#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Deploy a published release to an existing environment; never build artifacts.

The public report is an allowlist. Rendered configuration, Kubernetes objects,
and diagnostic details stay in the private work directory, never in artifacts.
"""

import argparse
import base64
from contextlib import redirect_stderr, redirect_stdout
import copy
from datetime import datetime, timezone
import hashlib
from fractions import Fraction
import json
import os
from pathlib import Path
import re
import sys
import time
from urllib.error import HTTPError
from urllib.parse import parse_qs, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

import yaml

try:
    from . import cetirun_platform_deploy as platform, deployment_artifacts as artifacts
except ImportError:  # Direct script invocation.
    import cetirun_platform_deploy as platform
    import deployment_artifacts as artifacts


CONFIG_KEYS = {"target", "release", "domain", "identityRealm", "ownerAlias", "identity",
               "istio", "broker", "platform", "portal", "sharedSkills", "skillMarketplace",
               "observability", "modelRouting", "publicTLS"}
PRESERVED = ("agents.cetirun.microsoft.com", "teams.cetirun.microsoft.com",
             "agentpolicies.cetirun.microsoft.com", "sandboxes.cetirun.microsoft.com", "pvc")


class Blocked(ValueError):
    """A stable, publishable error code containing no environment data."""


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def private_write(path, content):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with open(path, "w", opener=lambda name, flags: os.open(name, flags, 0o600)) as stream:
        stream.write(content)
    path.chmod(0o600)


def credentials(directory, config_text, kube_text):
    """Accept raw or base64 kubeconfig, without shell evaluation or exec plugins."""
    if not config_text or not kube_text:
        raise Blocked("environment_inputs_missing")
    try:
        config = yaml.safe_load(config_text)
        kube = yaml.safe_load(kube_text)
        if not isinstance(kube, dict):
            kube_text = base64.b64decode(kube_text, validate=True).decode()
            kube = yaml.safe_load(kube_text)
        if not isinstance(config, dict) or not isinstance(kube, dict):
            raise ValueError()
    except (ValueError, UnicodeError, yaml.YAMLError):
        raise Blocked("invalid_environment_yaml") from None
    if set(config) - CONFIG_KEYS:
        raise Blocked("unknown_environment_fields")
    reject_inline_credentials(config)
    # This pipeline only operates existing installations. No Azure identity,
    # DNS, cert-manager installation, TLS bootstrap, or external shell plugins.
    if config.get("publicTLS", {}).get("enabled"):
        raise Blocked("public_tls_bootstrap_out_of_scope")
    target = config.get("target", {})
    selected = next((item.get("context", {}) for item in kube.get("contexts", [])
                     if item.get("name") == target.get("context")), None)
    if not selected:
        raise Blocked("kubeconfig_context_missing")
    cluster = next((item.get("cluster", {}) for item in kube.get("clusters", [])
                    if item.get("name") == selected.get("cluster")), {})
    user = next((item.get("user", {}) for item in kube.get("users", [])
                 if item.get("name") == selected.get("user")), {})
    if (selected.get("cluster") != target.get("cluster") or cluster.get("server") != target.get("server")
            or not target.get("clusterUID")):
        raise Blocked("kubeconfig_target_mismatch")
    if (cluster.get("insecure-skip-tls-verify") or not cluster.get("certificate-authority-data")
            or set(cluster) - {"server", "certificate-authority-data"}
            or set(user) - {"client-certificate-data", "client-key-data", "token"}
            or not (user.get("token") or (user.get("client-certificate-data") and user.get("client-key-data")))):
        raise Blocked("kubeconfig_requires_embedded_tls_credentials")
    # Discard unrelated contexts/users instead of granting any plugin execution.
    isolated = {"apiVersion": "v1", "kind": "Config", "current-context": target["context"],
                "contexts": [{"name": target["context"], "context": selected}],
                "clusters": [{"name": target["cluster"], "cluster": cluster}],
                "users": [{"name": selected["user"], "user": user}]}
    private_write(directory / "kubeconfig", yaml.safe_dump(isolated))
    config = copy.deepcopy(config)
    config["target"]["kubeconfig"] = str((directory / "kubeconfig").resolve())
    return config


def reject_inline_credentials(value, path=()):
    """Credential values belong in existing Secrets, including provider env refs."""
    if isinstance(value, dict):
        for key, item in value.items():
            field = key.replace("_", "").replace("-", "").lower()
            secret_field = field in {"password", "clientsecret", "cookiesecret", "privatekey", "accesskey",
                                     "apikey", "token", "openclawgatewaytoken", "sshprivatekey"}
            secret_field |= key == "key" and "tls" in path
            if secret_field and isinstance(item, str) and item:
                if not (field == "apikey" and re.fullmatch(r"os\.environ/[A-Z][A-Z0-9_]*", item)):
                    raise Blocked("inline_application_credentials_forbidden")
            reject_inline_credentials(item, (*path, key))
    elif isinstance(value, list):
        for item in value:
            reject_inline_credentials(item, path)
    elif isinstance(value, str) and "PRIVATE KEY-----" in value:
        raise Blocked("inline_application_credentials_forbidden")


def release_snapshot(deployment, *, require_deployed=True):
    result = {}
    for name in platform.deployment_charts(deployment.config):
        section = deployment.config[name]
        rows = json.loads(deployment.helm("list", "--namespace", section["namespace"], "--all",
                                          "--selector", "status!=superseded", "-o", "json"))
        row = next((row for row in rows if row["name"] == section["release"]), None)
        if not row or (require_deployed and row["status"] != "deployed"):
            raise Blocked("existing_helm_release_not_deployed")
        result[name] = {"revision": str(row["revision"]), "chart": row["chart"], "status": row["status"]}
    return result


def managed_release_names(deployment):
    """Return releases this invocation can write.

    An externally managed Istio release is still observed and included in the
    preservation snapshot, but this deployment never renders or upgrades it.
    """
    istio = deployment.config.get("istio", {})
    return tuple(name for name in platform.deployment_charts(deployment.config)
                 if name != "istio" or istio.get("mode", "manage") == "manage")


def installation_claim_keys(deployment):
    """Return PVC identities referenced by the installed Helm manifests.

    Namespace membership alone is not an ownership boundary: unrelated
    workloads can legitimately use an installation namespace. StatefulSet
    claim-template prefixes are retained so existing ordinal claims remain in
    the preservation set even when a chart manifest contains only the template.
    """
    claims, prefixes = set(), []
    for name in platform.deployment_charts(deployment.config):
        section = deployment.config[name]
        raw = deployment.helm("get", "manifest", section["release"], "--namespace", section["namespace"])
        for document in yaml.safe_load_all(raw or ""):
            if not isinstance(document, dict):
                continue
            metadata = document.get("metadata", {})
            namespace = metadata.get("namespace", section["namespace"])
            kind = document.get("kind")
            if kind == "PersistentVolumeClaim":
                claims.add((namespace, metadata.get("name", "")))
            spec = document.get("spec", {})
            template = spec.get("template", {}).get("spec", {})
            if kind == "Pod":
                template = spec
            elif kind == "CronJob":
                template = spec.get("jobTemplate", {}).get("spec", {}).get("template", {}).get("spec", {})
            for volume in template.get("volumes", []):
                claim = volume.get("persistentVolumeClaim", {}).get("claimName")
                if claim:
                    claims.add((namespace, claim))
            if kind == "StatefulSet":
                statefulset = metadata.get("name", "")
                for template_claim in spec.get("volumeClaimTemplates", []):
                    claim_name = template_claim.get("metadata", {}).get("name", "")
                    if claim_name and statefulset:
                        prefixes.append((namespace, claim_name + "-" + statefulset + "-"))
                        start = spec.get("ordinals", {}).get("start", 0)
                        for ordinal in range(start, start + spec.get("replicas", 1)):
                            claims.add((namespace, f"{claim_name}-{statefulset}-{ordinal}"))
    return claims, tuple(prefixes)


def planned_claim_releases(deployment):
    """Only claims explicitly created by managed rendered resources may appear.

    Volume references to external claims are not permission to create claims.
    StatefulSet additions use the rendered ordinal range, never a name prefix.
    """
    claims = {}
    for name in managed_release_names(deployment):
        namespace = deployment.config[name]["namespace"]
        for document in managed_documents(deployment.documents[name]):
            meta, spec = document.get("metadata", {}), document.get("spec", {})
            ns = meta.get("namespace", namespace)
            if document.get("kind") == "PersistentVolumeClaim":
                keys = ["pvc/" + ns + "/" + meta["name"]]
            elif document.get("kind") == "StatefulSet":
                start = spec.get("ordinals", {}).get("start", 0)
                keys = [f"pvc/{ns}/{claim['metadata']['name']}-{meta['name']}-{ordinal}"
                        for claim in spec.get("volumeClaimTemplates", [])
                        for ordinal in range(start, start + spec.get("replicas", 1))]
            else:
                continue
            for key in keys:
                if key in claims and claims[key] != name:
                    raise Blocked("planned_claim_release_ambiguous")
                claims[key] = name
    return claims


def planned_claim_keys(deployment):
    return set(planned_claim_releases(deployment))


def capture(deployment, *, require_deployed=True):
    """Read exact identities and spec hashes; do not save credentials or HOME."""
    snapshot = {"objects": {}, "releases": {}}
    runtime_namespaces = set()
    namespace_agents = {}
    agent_uids = set()
    agent_namespaces = set()
    candidate_namespaces = {}
    for resource in PRESERVED:
        rows = json.loads(deployment.kubectl("get", resource, "--all-namespaces", "-o", "json"))["items"]
        for row in rows:
            meta, spec, status = row["metadata"], row.get("spec", {}), row.get("status", {})
            if resource == "agents.cetirun.microsoft.com" and status.get("namespace"):
                runtime_namespaces.add(status["namespace"])
                namespace_agents[status["namespace"]] = meta["uid"]
                agent_namespaces.add(status["namespace"])
            if resource == "agents.cetirun.microsoft.com":
                agent_uids.add(meta.get("uid"))
                candidate_namespaces.setdefault("cetirun-" + meta["name"], set()).add(meta["uid"])
                if status.get("namespace"):
                    candidate_namespaces.setdefault(status["namespace"], set()).add(meta["uid"])
            key = "/".join((resource, meta.get("namespace", ""), meta["name"]))
            snapshot["objects"][key] = {
                "uid": meta["uid"], "specHash": digest(spec), "generation": meta.get("generation"),
                "deleting": bool(meta.get("deletionTimestamp")), "ready": agent_ready(row),
                "bound": status.get("phase") == "Bound" if resource == "pvc" else None,
                "agentType": spec.get("agent", {}).get("type", ""),
                "image": spec.get("agent", {}).get("image", ""),
                "chartVersion": spec.get("helm", {}).get("version", ""),
                "suspended": spec.get("suspended", False),
                "name": meta["name"],
                "customOwnershipRequired": "cetirun.microsoft.com/custom-runtime-ownership" in meta.get("finalizers", []),
            }
            if resource == "pvc":
                snapshot["objects"][key].update(
                    claimContractHash=digest({field: value for field, value in spec.items() if field != "volumeName"}),
                    volumeName=spec.get("volumeName", ""),
                    homeStorage={"storageClass": spec.get("storageClassName", ""),
                                 "storageSize": spec.get("resources", {}).get("requests", {}).get("storage", ""),
                                 "accessModes": spec.get("accessModes", [])})
    # status.namespace is written only after the runtime becomes ready. Resolve
    # an already-created namespace from the immutable Agent ownership metadata
    # so a Pod cannot be missed during that interval.
    if agent_uids:
        namespaces = json.loads(deployment.kubectl("get", "namespace", "-o", "json"))["items"]
        for namespace in namespaces:
            meta = namespace.get("metadata", {})
            owners = meta.get("ownerReferences", [])
            annotations = meta.get("annotations", {})
            owner_uids = {owner.get("uid") for owner in owners
                          if owner.get("apiVersion") == "cetirun.microsoft.com/v1alpha1" and
                          owner.get("kind") == "Agent" and owner.get("uid") in agent_uids}
            annotation_uid = annotations.get("cetirun.microsoft.com/agent-uid")
            annotation_match = annotation_uid in agent_uids
            conflicting_owner = bool(owners) and (len(owners) != 1 or len(owner_uids) != 1)
            if ((owner_uids or annotation_match) and conflicting_owner or
                    owner_uids and annotation_uid and annotation_uid not in owner_uids):
                raise Blocked("agent_namespace_ownership_conflict")
            attributed_uid = next(iter(owner_uids), annotation_uid if annotation_match else None)
            candidate_uids = candidate_namespaces.get(meta.get("name"), set())
            # Portal creates the namespace before it binds Agent ownership. A
            # failed binding patch must not hide an already running workload.
            # Do not adopt or repair that namespace during platform deployment.
            if candidate_uids and candidate_uids != {attributed_uid}:
                raise Blocked("agent_namespace_unattributed")
            if attributed_uid:
                runtime_namespaces.add(meta.get("name"))
                namespace_agents[meta.get("name")] = attributed_uid
                agent_namespaces.add(meta.get("name"))
    # PVCs from unrelated workloads must not make an otherwise safe upgrade
    # fail. Retain only claims referenced by installed manifests and claims in
    # Agent-owned namespaces.
    installation_claims, claim_prefixes = installation_claim_keys(deployment)
    planned_claims = planned_claim_keys(deployment)
    # Only explicit references and the installed StatefulSet ordinal range
    # establish prior use. A template name prefix alone does not prove that an
    # existing claim at a newly requested ordinal belongs to this installation.
    snapshot["installedClaimKeys"] = sorted("pvc/" + ns + "/" + name for ns, name in installation_claims)
    pvc_prefix = "pvc/"
    for key in list(snapshot["objects"]):
        if key.startswith(pvc_prefix):
            namespace = key.split("/", 2)[1]
            name = key.split("/", 2)[2]
            scoped = key in planned_claims or namespace in agent_namespaces or (namespace, name) in installation_claims or any(
                namespace == prefix_namespace and name.startswith(prefix) and name[len(prefix):].isdigit()
                for prefix_namespace, prefix in claim_prefixes)
            if not scoped:
                del snapshot["objects"][key]
    # A controller can replace runtime workloads without changing Agent specs.
    # Include them so a sidecar/default rollout cannot masquerade as preservation.
    for namespace in sorted(runtime_namespaces):
        rows = json.loads(deployment.kubectl("get", "pods", "--namespace", namespace, "-o", "json"))["items"]
        for row in rows:
            meta = row["metadata"]
            statuses = row.get("status", {}).get("containerStatuses", [])
            labels = meta.get("labels", {})
            is_proxy = (labels.get("app.kubernetes.io/component") == "copilot-proxy" or
                        labels.get("app.kubernetes.io/name") == "cetirun-copilot-proxy")
            proxies = [container.get("image", "") for container in row["spec"].get("containers", [])
                       if container.get("name") == "proxy"]
            if is_proxy and (len(proxies) != 1 or not proxies[0]):
                raise Blocked("copilot_proxy_image_unavailable")
            snapshot["objects"]["pods/" + namespace + "/" + meta["name"]] = {
                "uid": meta["uid"], "specHash": digest(row["spec"]), "generation": meta.get("generation"),
                "deleting": bool(meta.get("deletionTimestamp")),
                "ready": bool(statuses) and all(item.get("ready") for item in statuses), "bound": None,
                "agentType": "", "image": "", "suspended": False,
                "copilotProxyImage": proxies[0] if is_proxy else None,
                "runtimeAgentUID": namespace_agents.get(namespace),
                "isRuntimePod": any(container.get("name") == "cetirun" for container in row["spec"].get("containers", [])),
                "memoryInitImages": [container.get("image", "") for container in row["spec"].get("initContainers", [])
                                     if container.get("name") == "memory-assets"],
            }
    snapshot["releases"] = release_snapshot(deployment, require_deployed=require_deployed)
    return snapshot


def verify_planned_claim_ownership(deployment, snapshot):
    planned = planned_claim_keys(deployment)
    installed = set(snapshot.get("installedClaimKeys", []))
    if (installed & planned) - snapshot["objects"].keys():
        raise Blocked("installed_claim_missing")
    existing_planned = planned & snapshot["objects"].keys()
    if existing_planned - installed:
        raise Blocked("existing_planned_claim_unattributed")


def agent_ready(row):
    meta, status = row.get("metadata", {}), row.get("status", {})
    return (status.get("observedGeneration") == meta.get("generation") and
            any(c.get("type") == "Ready" and c.get("status") == "True" for c in status.get("conditions", [])))


def verify_existing_agent_images(deployment, snapshot, roots, lock):
    """Match the Operator's exact image approvals without migrating any pin."""
    profiles = {}
    approved = platform.get_value(deployment.values["platform"], "cetirun-operator.customRuntime.approvedImages") or []
    for item in snapshot["objects"].values():
        runtime = item.get("agentType")
        if runtime == "custom" and item["image"] not in approved:
            raise Blocked("existing_custom_image_not_approved")
        if deployment.config.get("_infrastructureOnly"):
            # An unchanged runtime is qualified by its retained chart, not by
            # the unrelated runtime chart bundled with this control-plane
            # release. Exact runtime configuration and live objects are
            # checked separately before every write and during acceptance.
            continue
        if runtime not in ("openclaw", "hermes") or item.get("chartVersion", "") not in ("", lock["version"]):
            continue
        if runtime not in profiles:
            profile = yaml.safe_load((roots["cetirun"] / "agents" / runtime / "profile.yaml").read_text())
            qualified = profile.get("qualifiedImages", [])
            if not isinstance(qualified, list) or not all(isinstance(image, str) for image in qualified):
                raise Blocked("runtime_chart_profile_invalid")
            # verify_runtime_chart already requires the same repository, tag,
            # digest and appVersion as this lock. Go accepts the exact default
            # spelling or exact qualifiedImages entries, not digest aliases.
            profiles[runtime] = {lock["images"][runtime + "-gateway"]["reference"], *qualified}
        if item["image"] not in profiles[runtime]:
            raise Blocked("existing_agent_image_not_qualified")


def rendered_operator(deployment):
    for document in deployment.documents["platform"]:
        if document.get("kind") == "Deployment" and document.get("metadata", {}).get("name") == "cetirun-operator":
            return next((container for container in document["spec"]["template"]["spec"]["containers"]
                         if container.get("name") == "manager"), {})
    return {}


def runtime_arguments(arguments):
    """Read only the runtime controls, without disclosing other arguments."""
    names = {"chart-repo-url", "chart-version", "chart-digest", "memory-context-image", "memory-context-enabled",
             "domain-suffix", "istio-gateway", "default-model"}
    if not isinstance(arguments, list) or not all(isinstance(value, str) for value in arguments):
        raise Blocked("operator_runtime_arguments_unavailable")
    result = {}
    index = 0
    while index < len(arguments):
        name, separator, value = arguments[index].removeprefix("--").partition("=")
        if name in names:
            if name in result:
                raise Blocked("operator_runtime_arguments_ambiguous")
            if not separator:
                index += 1
                if index >= len(arguments) or arguments[index].startswith("--"):
                    raise Blocked("operator_runtime_arguments_unavailable")
                value = arguments[index]
            result[name] = value
        index += 1
    if (not result.get("chart-repo-url") or not result.get("chart-version") or
            result.get("memory-context-enabled") not in ("true", "false")):
        raise Blocked("operator_runtime_arguments_unavailable")
    return result


def current_operator_arguments(deployment):
    namespace = deployment.config["platform"]["namespace"]
    before = deployment.resource("deployment", "cetirun-operator", namespace)
    if not before or not before.get("uid") or before.get("deletionTimestamp"):
        raise Blocked("operator_runtime_arguments_unavailable")
    prefix = 'spec.template.spec.containers[?(@.name=="manager")]'
    raw = deployment.selected_fields("deployment", "cetirun-operator", (prefix + ".args",), namespace)[0]
    # jsonpath-as-json retains an array even for one matching environment entry.
    values = deployment.kubectl("get", "deployment", "cetirun-operator", "--namespace", namespace, "-o",
                                'jsonpath-as-json={.' + prefix + '.env[?(@.name=="CETIRUN_HELM_VALUES")]}')
    try:
        arguments = runtime_arguments(json.loads(raw))
        arguments["helm-values"] = runtime_helm_values(json.loads(values or "[]"))
    except (ValueError, TypeError) as error:
        raise Blocked("operator_runtime_arguments_unavailable") from error
    after = deployment.resource("deployment", "cetirun-operator", namespace)
    if not after or any(before.get(key) != after.get(key) for key in ("uid", "resourceVersion")):
        raise Blocked("operator_runtime_arguments_changed")
    return arguments


def runtime_helm_values(entries):
    if not isinstance(entries, list) or not all(isinstance(entry, dict) for entry in entries):
        raise Blocked("operator_runtime_values_unavailable")
    values = [entry for entry in entries if entry.get("name") == "CETIRUN_HELM_VALUES"]
    if len(values) > 1 or any("valueFrom" in entry or not isinstance(entry.get("value"), str) for entry in values):
        raise Blocked("operator_runtime_values_unavailable")
    try:
        result = json.loads(values[0]["value"] or "{}") if values else {}
    except ValueError as error:
        raise Blocked("operator_runtime_values_unavailable") from error
    if not isinstance(result, dict):
        raise Blocked("operator_runtime_values_unavailable")
    return result


def runtime_environment(entries):
    """Compare accepted runtime settings without recording their private values."""
    if not isinstance(entries, list) or not all(isinstance(entry, dict) for entry in entries):
        raise Blocked("operator_runtime_environment_unavailable")
    result = {}
    json_values = {"CETIRUN_HELM_VALUES", "CETIRUN_CHAT_ACCESS", "CETIRUN_CUSTOM_RUNTIME",
                   "CETIRUN_SANDBOX_SOURCES", "CETIRUN_SKILL_MARKETPLACE"}
    # These settings feed Agent reconciliation. Process settings, logging and
    # the Operator binary identity may change with a control-plane release.
    protected = json_values | {
        "ISTIO_REVISION", "AI_GATEWAY_COPILOT_PROXY_IMAGE", "AI_GATEWAY_COPILOT_HEALTH_CHECKS",
        "AI_GATEWAY_DEFAULT_BUDGET", "AI_GATEWAY_DEFAULT_MODEL", "AI_GATEWAY_ENABLED",
        "AI_GATEWAY_MASTER_KEY_SECRET", "AI_GATEWAY_MASTER_KEY_SECRET_KEY", "AI_GATEWAY_URL",
        "AGENT_ID_BLUEPRINT_CLIENT_ID", "AGENT_ID_BROKER_URL", "AGENT_ID_ENABLED",
        "AGENT_ID_NAME_PREFIX", "AGENT_ID_TENANT_ID", "AGENT_ID_CLEANUP_ENABLED",
        "CONNECTIONS_MCP_BROKER_NAMESPACE", "CONNECTIONS_MCP_BROKER_SERVICE_ACCOUNT",
        "CONNECTIONS_MCP_CA_SECRET_NAME", "CONNECTIONS_MCP_NATIVE_ENABLED",
        "CONNECTIONS_MCP_RUNTIME_TYPES", "CONNECTIONS_MCP_URL", "WORKLOAD_TOKEN_AUDIENCE",
        "REGISTRY_CREDENTIAL_NAMESPACE",
    }
    seen = set()
    for entry in entries:
        name = entry.get("name")
        if not isinstance(name, str) or not name or name in seen:
            raise Blocked("operator_runtime_environment_unavailable")
        seen.add(name)
        if name not in protected:
            continue
        value = {key: copy.deepcopy(item) for key, item in entry.items() if key != "name"}
        if name in json_values and "value" in value:
            try:
                value["value"] = json.loads(value["value"])
            except (ValueError, TypeError):
                raise Blocked("operator_runtime_environment_unavailable") from None
        result[name] = value
    return result


def verify_runtime_environment(deployment, operator):
    namespace = deployment.config["platform"]["namespace"]
    before = deployment.resource("deployment", "cetirun-operator", namespace)
    if not before or not before.get("uid") or before.get("deletionTimestamp"):
        raise Blocked("operator_runtime_environment_unavailable")
    raw = deployment.kubectl("get", "deployment", "cetirun-operator", "--namespace", namespace,
                             "-o", 'jsonpath-as-json={.spec.template.spec.containers[?(@.name=="manager")].env[*]}')
    try:
        current = runtime_environment(json.loads(raw))
    except (ValueError, TypeError):
        raise Blocked("operator_runtime_environment_unavailable") from None
    after = deployment.resource("deployment", "cetirun-operator", namespace)
    if not after or any(before.get(key) != after.get(key) for key in ("uid", "resourceVersion")):
        raise Blocked("operator_runtime_environment_changed")
    if digest(current) != digest(runtime_environment(operator.get("env", []))):
        raise Blocked("existing_runtime_environment_transition_out_of_scope")


def verify_operator_runtime_transition(deployment, snapshot, operator):
    agents = [item for item in snapshot["objects"].values() if item.get("agentType")]
    if not agents and not deployment.config.get("_infrastructureOnly"):
        return
    current = current_operator_arguments(deployment)
    candidate = runtime_arguments(operator.get("args", []))
    if digest(current["helm-values"]) != digest(runtime_helm_values(operator.get("env", []))):
        raise Blocked("existing_runtime_values_transition_out_of_scope")
    old_image, new_image = (values.get("memory-context-image", "") for values in (current, candidate))
    old_enabled = current["memory-context-enabled"] == "true" or bool(old_image)
    new_enabled = candidate["memory-context-enabled"] == "true" or bool(new_image)
    if old_image != new_image or old_enabled != new_enabled:
        raise Blocked("existing_memory_override_transition_out_of_scope")
    if current["chart-repo-url"] != candidate["chart-repo-url"]:
        raise Blocked("existing_chart_repository_transition_out_of_scope")
    old_version, new_version = current["chart-version"], candidate["chart-version"]
    if deployment.config.get("_infrastructureOnly"):
        if old_version != new_version or current.get("chart-digest", "") != candidate.get("chart-digest", ""):
            raise Blocked("existing_runtime_chart_transition_out_of_scope")
        if any(current.get(field, "") != candidate.get(field, "")
               for field in ("domain-suffix", "istio-gateway", "default-model")):
            raise Blocked("existing_runtime_arguments_transition_out_of_scope")
        verify_runtime_environment(deployment, operator)
        return
    if old_version == new_version:
        return
    if any(not item.get("chartVersion") for item in agents):
        raise Blocked("implicit_agent_chart_upgrade_out_of_scope")
    if not any(item["chartVersion"] == old_version for item in agents):
        return
    # The runtime pins only its configured default version. Verify the tag
    # that retained Agents will use when the platform changes that default.
    # This proves content at deployment time, not future tag immutability.
    repository = artifacts.PREFIX + "/helm/cetirun"
    expected_repo = "oci://" + artifacts.REGISTRY + "/" + repository
    old_digest = current.get("chart-digest", "")
    if (current["chart-repo-url"] != expected_repo or candidate["chart-repo-url"] != expected_repo or
            not re.fullmatch(r"sha256:[0-9a-f]{64}", old_digest) or
            not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", old_version)):
        raise Blocked("retained_chart_digest_unverified")
    try:
        actual, _, _ = artifacts._manifest(repository, old_version, "chart")
    except artifacts.ArtifactError as error:
        raise Blocked("retained_chart_digest_unverified") from error
    if actual != old_digest:
        raise Blocked("retained_chart_tag_changed")


def verify_memory_runtime_images(snapshot, operator, roots, lock):
    """Compare effective chart Memory images before an Agent can reconcile."""
    affected = {item["uid"] for item in snapshot["objects"].values()
                if item.get("agentType") and item.get("chartVersion", "") in ("", lock["version"])}
    pods = [item for item in snapshot["objects"].values()
            if item.get("runtimeAgentUID") in affected and (item.get("isRuntimePod") or item.get("memoryInitImages"))]
    if not pods:
        return
    arguments = runtime_arguments(operator.get("args", []))
    image = arguments.get("memory-context-image", "")
    enabled = arguments["memory-context-enabled"] == "true" or bool(image)
    if enabled and not image:
        values = yaml.safe_load((roots["cetirun"] / "values.yaml").read_text())
        image = values.get("memoryContext", {}).get("image", "")
    expected = [image] if enabled else []
    if (enabled and not image) or any(item["memoryInitImages"] != expected for item in pods):
        raise Blocked("existing_memory_init_rollout_out_of_scope")


def storage_quantity(value):
    """Compare retained Kubernetes storage quantities without rounding units."""
    match = re.fullmatch(r"([+]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+))([eE][+-]?[0-9]+|[KMGTPE]i|[numkMGTPE]?)", str(value))
    if not match or len(str(value)) > 64:
        raise Blocked("custom_home_storage_unverified")
    number, suffix = match.groups()
    if suffix.endswith("i"):
        scale = Fraction(1024) ** ("KMGTPE".index(suffix[0]) + 1)
    else:
        exponent = int(suffix[1:]) if len(suffix) > 1 else {"n": -9, "u": -6, "m": -3, "": 0,
                      "k": 3, "M": 6, "G": 9, "T": 12, "P": 15, "E": 18}[suffix]
        if abs(exponent) > 63:
            raise Blocked("custom_home_storage_unverified")
        scale = Fraction(10) ** exponent
    result = Fraction(number) * scale
    if result <= 0:
        raise Blocked("custom_home_storage_unverified")
    return result


def verify_custom_home_preservation(deployment, snapshot):
    """Retained provider HOME must remain usable with the candidate settings."""
    agents = [item for item in snapshot["objects"].values() if item.get("agentType") == "custom"]
    if not agents:
        return
    namespace = deployment.config["platform"]["namespace"]
    desired = platform.get_value(deployment.values["platform"], "cetirun-operator.customRuntime") or {}
    for item in agents:
        name = item["name"]
        record = "custom-ownership-" + hashlib.sha256(item["uid"].encode()).hexdigest()[:32]
        before = deployment.resource("configmap", record, namespace)
        if before is None:
            if item.get("customOwnershipRequired"):
                raise Blocked("custom_home_binding_unavailable")
            # Sandbox Custom does not use this provider. A new ordinary Custom
            # Agent may also not have established its first binding yet.
            continue
        raw, immutable = deployment.selected_fields("configmap", record, ("data.ownership", "immutable"), namespace)
        after = deployment.resource("configmap", record, namespace)
        if (not before.get("uid") or before.get("deletionTimestamp") or not after or
                any(before.get(key) != after.get(key) for key in ("uid", "resourceVersion"))):
            raise Blocked("custom_home_binding_changed")
        try:
            receipt = json.loads(raw)
        except (ValueError, TypeError):
            raise Blocked("custom_home_binding_unavailable") from None
        runtime_namespace = "cetirun-" + name
        home = "data-" + runtime_namespace[:63].rstrip("-") + "-0"
        owners = before.get("ownerReferences", [])
        valid_owner = (len(owners) == 1 and owners[0].get("apiVersion") == "cetirun.microsoft.com/v1alpha1"
                       and owners[0].get("kind") == "Agent" and owners[0].get("name") == name
                       and owners[0].get("uid") == item["uid"])
        if (not isinstance(receipt, dict) or immutable != "true" or not valid_owner or
                any(receipt.get(key) != value for key, value in {
                    "agentName": name, "agentUID": item["uid"], "namespace": runtime_namespace,
                    "release": runtime_namespace, "home": home}.items()) or not receipt.get("namespaceUID")):
            raise Blocked("custom_home_binding_unavailable")
        live_namespace = deployment.resource("namespace", runtime_namespace)
        if (not live_namespace or live_namespace.get("uid") != receipt["namespaceUID"] or
                live_namespace.get("deletionTimestamp")):
            raise Blocked("custom_home_binding_changed")
        claim = receipt.get("homeClaim")
        if not isinstance(claim, dict) or set(claim) != {"storageClass", "storageSize", "accessMode"}:
            raise Blocked("custom_home_storage_unverified")
        if (not desired.get("enabled") or claim["storageClass"] != desired.get("storageClass") or
                claim["accessMode"] != desired.get("accessMode") or
                storage_quantity(claim["storageSize"]) != storage_quantity(desired.get("storageSize"))):
            raise Blocked("existing_custom_home_transition_out_of_scope")
        pvc = snapshot["objects"].get("pvc/" + runtime_namespace + "/" + home)
        if pvc is not None:
            storage = pvc.get("homeStorage", {})
            if (storage.get("storageClass") != claim["storageClass"] or
                    storage.get("accessModes") != [claim["accessMode"]] or
                    storage_quantity(storage.get("storageSize")) != storage_quantity(claim["storageSize"])):
                raise Blocked("custom_home_storage_unverified")


def verify_cetiadmin_transition(deployment, operator, lock):
    """A platform upgrade cannot implicitly restart a preserved installation."""
    namespace = deployment.config["platform"]["namespace"]
    source = deployment.resource("configmap", platform.CETIADMIN_SOURCE, namespace)
    desired = next((json.loads(document["data"]["config.json"]) for document in deployment.documents["platform"]
                    if document.get("kind") == "ConfigMap" and
                    document.get("metadata", {}).get("name") == platform.CETIADMIN_SOURCE), None)
    if source is None:
        binding_name = "cetiadmin-binding-" + hashlib.sha256((namespace + "/" + platform.CETIADMIN_SOURCE).encode()).hexdigest()[:24]
        if deployment.resource("configmap", binding_name, namespace) is not None:
            raise Blocked("cetiadmin_stop_unverified")
        if desired and desired.get("enabled"):
            raise Blocked("cetiadmin_installation_transition_out_of_scope")
        return
    try:
        _, _, current, state = deployment.cetiadmin_binding()
        if desired is None:
            raise Blocked("cetiadmin_installation_transition_out_of_scope")
        if not desired.get("enabled"):
            # Verify the current disabled installation, not the next release's
            # coordinates. This is a read-only stop check, not a disable action.
            deployment.check_cetiadmin_stopped(configuration=current)
        elif (platform.cetiadmin_config_hash(desired) != state.get("configHash") or
              operator.get("image") != state.get("operatorImage") or
              lock["version"] != state.get("runtimeVersion")):
            raise Blocked("cetiadmin_installation_transition_out_of_scope")
    except ValueError as error:
        if isinstance(error, Blocked):
            raise
        raise Blocked("cetiadmin_stop_unverified") from error


def verify_runtime_preservation(deployment, snapshot, roots, lock):
    verify_existing_agent_images(deployment, snapshot, roots, lock)
    operator = rendered_operator(deployment)
    verify_operator_runtime_transition(deployment, snapshot, operator)
    if not deployment.config.get("_infrastructureOnly"):
        verify_memory_runtime_images(snapshot, operator, roots, lock)
    verify_custom_home_preservation(deployment, snapshot)
    proxy_image = next((item.get("value", "") for item in operator.get("env", [])
                        if item.get("name") == "AI_GATEWAY_COPILOT_PROXY_IMAGE"), "")
    for item in snapshot["objects"].values():
        image = item.get("copilotProxyImage")
        if image is not None and (not proxy_image or image != proxy_image):
            raise Blocked("existing_copilot_proxy_rollout_out_of_scope")
    verify_cetiadmin_transition(deployment, operator, lock)


def reject_downgrade(releases, candidate, names=None):
    requested = tuple(int(part) for part in candidate.split("."))
    selected = releases if names is None else {name: releases[name] for name in names}
    for release in selected.values():
        current = re.search(r"-(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$", release.get("chart", ""))
        if current is None:
            raise Blocked("existing_release_version_unrecognized")
        if requested < tuple(int(part) for part in current.groups()):
            raise Blocked("release_downgrade_out_of_scope")


def preservation(before, after, *, readiness=True, allowed_additions=frozenset()):
    old_keys, new_keys = set(before["objects"]), set(after["objects"])
    if old_keys - new_keys or (new_keys - old_keys) - set(allowed_additions):
        raise Blocked("preserved_resource_set_changed")
    for key, old in before["objects"].items():
        new = after["objects"][key]
        if any(old[field] != new[field] for field in ("uid", "specHash", "generation", "deleting")):
            raise Blocked("preserved_resource_changed")
        if readiness and ((old["ready"] and not new["ready"]) or (old["bound"] and not new["bound"])):
            raise Blocked("preserved_resource_not_ready")


def observe_preservation(baseline, observed, current, *, allowed_additions=frozenset(), readiness=True):
    """Carry first-observed claims through every upgrade and acceptance read."""
    for key in observed["objects"].keys() & current["objects"].keys() & set(allowed_additions):
        previous, latest = observed["objects"][key], current["objects"][key]
        if key not in baseline["objects"] and not previous["bound"]:
            # Only initial binding can change a newly created claim's spec.
            # Its UID and all other storage fields must remain unchanged.
            if (previous.get("claimContractHash") and
                    previous["claimContractHash"] == latest.get("claimContractHash") and
                    previous.get("volumeName", "") in ("", latest.get("volumeName", ""))):
                previous["specHash"] = latest["specHash"]
                previous["volumeName"] = latest.get("volumeName", "")
    preservation(observed, current, readiness=readiness, allowed_additions=allowed_additions)
    for key in current["objects"].keys() - observed["objects"].keys():
        observed["objects"][key] = copy.deepcopy(current["objects"][key])
    for key in set(allowed_additions) & current["objects"].keys():
        if current["objects"][key]["bound"]:
            observed["objects"][key]["bound"] = True


def stable_acceptance(deployment, baseline, *, expected_releases=None, allowed_additions=frozenset(),
                      observed=None, window=60, timeout=300, interval=15):
    """Observe several reconciles; never excuse UID/spec drift during settling."""
    start = time.monotonic()
    stable_since = None
    observed = copy.deepcopy(baseline) if observed is None else observed
    while time.monotonic() - start <= timeout:
        current = capture(deployment)
        observe_preservation(baseline, observed, current, readiness=False, allowed_additions=allowed_additions)
        if expected_releases is not None and current["releases"] != expected_releases:
            raise Blocked("helm_revision_changed_during_acceptance")
        try:
            preservation(observed, current)
            if any(key not in current["objects"] or not current["objects"][key]["bound"] or
                   current["objects"][key]["deleting"] for key in allowed_additions):
                raise Blocked("planned_claim_not_ready")
            verify_control_readiness(deployment)
        except Blocked:
            stable_since = None
        else:
            if stable_since is None:
                stable_since = time.monotonic()
            if time.monotonic() - stable_since >= window:
                return current
        time.sleep(interval)
    raise Blocked("post_deployment_stability_timeout")


def verify_control_readiness(deployment):
    """Observe all rendered controllers and their Pods throughout acceptance."""
    for name, documents in deployment.documents.items():
        default_namespace = deployment.config[name]["namespace"]
        for document in managed_documents(documents):
            kind = document.get("kind")
            if kind not in ("Deployment", "StatefulSet", "DaemonSet"):
                continue
            meta = document["metadata"]
            namespace = meta.get("namespace", default_namespace)
            live = json.loads(deployment.kubectl("get", kind, meta["name"], "--namespace", namespace, "-o", "json"))
            status, spec = live.get("status", {}), live.get("spec", {})
            desired = spec.get("replicas", 1)
            ready_fields = ("readyReplicas", "updatedReplicas")
            if kind == "Deployment":
                ready_fields += ("availableReplicas",)
            if kind == "DaemonSet":
                desired = status.get("desiredNumberScheduled", 0)
                ready_fields = ("numberReady", "updatedNumberScheduled", "numberAvailable")
            if (status.get("observedGeneration", 0) < live["metadata"].get("generation", 1) or
                    any(status.get(field, 0) < desired for field in ready_fields)):
                raise Blocked("control_plane_not_ready")
            labels = document["spec"]["selector"].get("matchLabels", {})
            if not labels:
                raise Blocked("control_plane_selector_unsupported")
            selector = ",".join(f"{key}={value}" for key, value in sorted(labels.items()))
            rows = json.loads(deployment.kubectl("get", "pods", "--namespace", namespace, "--selector", selector, "-o", "json"))["items"]
            pods = [pod for pod in rows if not pod["metadata"].get("deletionTimestamp")]
            if len(pods) < desired or any(
                    pod.get("status", {}).get("phase") != "Running" or not any(
                        condition.get("type") == "Ready" and condition.get("status") == "True"
                        for condition in pod.get("status", {}).get("conditions", [])) for pod in pods):
                raise Blocked("control_plane_not_ready")


def object_key(document, namespace):
    meta = document.get("metadata", {})
    return (document["apiVersion"], document["kind"], meta.get("namespace", namespace), meta["name"])


def managed_documents(documents):
    return [doc for doc in documents if doc and "helm.sh/hook" not in doc.get("metadata", {}).get("annotations", {})]


CONTROL_PLANE_IMAGES = {"cetirun-operator", "cetirun-portal", "cetirun-ai-gateway",
                        "cetirun-agent-broker", "cetirun-connections-mcp"}


def published_workload_image(deployment, image, lock):
    basename = image.split("@")[0].rsplit("/", 1)[-1].split(":")[0]
    if deployment.config.get("_infrastructureOnly") is True and basename not in CONTROL_PLANE_IMAGES:
        return None
    return lock["images"].get(basename)


def workload_images(document):
    template = document.get("spec", {}).get("template", {}).get("spec", {})
    return {(field, container["name"]): container.get("image", "")
            for field in ("containers", "initContainers") for container in template.get(field, [])}


def compare_manifests(deployment):
    """Reject resource removals and storage changes; publish counts, not values."""
    changes = {}
    for name in managed_release_names(deployment):
        section = deployment.config[name]
        previous = list(yaml.safe_load_all(deployment.helm("get", "manifest", section["release"], "--namespace", section["namespace"])))
        old = {object_key(doc, section["namespace"]): doc for doc in managed_documents(previous)}
        new = {object_key(doc, section["namespace"]): doc for doc in managed_documents(deployment.documents[name])}
        if set(old) - set(new):
            raise Blocked("render_removes_existing_resource")
        for key in old.keys() & new.keys():
            if key[1] == "StatefulSet" and old[key].get("spec", {}).get("volumeClaimTemplates") != new[key].get("spec", {}).get("volumeClaimTemplates"):
                raise Blocked("statefulset_storage_contract_changed")
            if key[1] in ("PersistentVolumeClaim", "PersistentVolume", "StorageClass") and old[key].get("spec") != new[key].get("spec"):
                raise Blocked("storage_contract_changed")
        if deployment.config.get("_infrastructureOnly") is True:
            for key, document in new.items():
                previous = workload_images(old.get(key, {}))
                for container, image in workload_images(document).items():
                    basename = image.split("@")[0].rsplit("/", 1)[-1].split(":")[0]
                    if basename not in CONTROL_PLANE_IMAGES and image != previous.get(container):
                        raise Blocked("auxiliary_image_transition_out_of_scope")
        changes[name] = {"added": len(new.keys() - old.keys()), "changed": sum(old[key] != new[key] for key in old.keys() & new.keys()),
                         "removed": 0}
    return changes


def audit_rendered_images(deployment, lock):
    """Catch new or unsupported image value shapes before any cluster write."""
    observed = set()
    for documents in deployment.documents.values():
        for document in documents:
            for item in platform.walk(document):
                for field in ("containers", "initContainers"):
                    for container in item.get(field, []):
                        image = container.get("image", "")
                        name = image.split("@")[0].rsplit("/", 1)[-1].split(":")[0]
                        if image.startswith(lock["registry"] + "/") and name not in lock["images"]:
                            raise Blocked("rendered_image_missing_from_inventory")
                        if published_workload_image(deployment, image, lock):
                            if image != lock["images"][name]["reference"]:
                                raise Blocked("rendered_image_not_locked")
                            observed.add(name)
    required = CONTROL_PLANE_IMAGES
    if not required <= observed:
        raise Blocked("required_control_plane_images_missing")
    return len(observed)


def verify_runtime_chart(roots, lock):
    """The Operator reads runtime profiles from this published chart itself."""
    values = yaml.safe_load((roots["cetirun"] / "values.yaml").read_text())
    for section, image in (("openclaw", "openclaw-gateway"), ("hermes", "hermes-gateway"),
                           ("desktop", "cetirun-desktop"), ("ttyd", "cetirun-terminal")):
        block = values.get(section, {}).get("image", {})
        if (block.get("digest") != lock["images"][image]["digest"] or
                block.get("repository") != lock["registry"] + "/" + image or
                block.get("tag") not in ("", None, lock["version"])):
            raise Blocked("runtime_chart_digest_pins_missing")
        if section in ("openclaw", "hermes"):
            profile = yaml.safe_load((roots["cetirun"] / "agents" / section / "profile.yaml").read_text())
            qualified = profile.get("qualifiedImages", [])
            pinned = lock["registry"] + "/" + image + "@" + lock["images"][image]["digest"]
            if pinned not in qualified and lock["images"][image]["reference"] not in qualified:
                raise Blocked("runtime_chart_profile_not_qualified")
    memory = values.get("memoryContext", {}).get("image", "")
    if memory != lock["images"]["cetirun-memory-context"]["reference"]:
        raise Blocked("runtime_chart_digest_pins_missing")
    return "PASS"


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def verify_entry(domain):
    """Require valid HTTPS and an authentication redirect, without logging URL data."""
    opener = build_opener(NoRedirect())
    request = Request("https://" + domain + "/", method="GET")
    try:
        with opener.open(request, timeout=30) as response:
            code, location = response.status, response.headers.get("Location", "")
    except HTTPError as error:
        code, location = error.code, error.headers.get("Location", "")
        error.close()
    if code not in (302, 303, 307, 308):
        raise Blocked("public_entry_auth_redirect_missing")
    target = urlsplit(location)
    same_origin = not target.netloc or target.netloc.lower() == domain.lower()
    local_login = same_origin and target.path in ("/oauth2/start", "/oauth2/sign_in")
    entra_login = (target.netloc == "login.microsoftonline.com" and
                   re.fullmatch(r"/[A-Za-z0-9.-]+/oauth2/(?:v2\.0/)?authorize", target.path))
    query = parse_qs(target.query, keep_blank_values=True)
    if local_login:
        for destination in query.get("rd", []):
            returned = urlsplit(destination)
            if (returned.scheme not in ("", "https") or
                    returned.netloc not in ("", domain) or not returned.path.startswith("/") or
                    destination.startswith("//") or "\\" in destination):
                local_login = False
    if entra_login:
        entra_login = query.get("redirect_uri") == ["https://" + domain + "/oauth2/callback"]
    if (not location or (target.scheme and target.scheme != "https") or
            target.username or target.password or not (local_login or entra_login)):
        raise Blocked("public_entry_redirect_invalid")
    return {"tls": "PASS", "authenticationRedirect": "PASS", "httpStatus": code}


def verify_images(deployment, lock):
    count = 0
    for name in platform.deployment_charts(deployment.config):
        section = deployment.config[name]
        rows = json.loads(deployment.kubectl("get", "pods", "--namespace", section["namespace"], "-o", "json"))["items"]
        for document in deployment.documents[name]:
            if document.get("kind") not in ("Deployment", "StatefulSet", "DaemonSet"):
                continue
            template = document["spec"]["template"]
            selector = document["spec"]["selector"].get("matchLabels", {})
            containers = template["spec"].get("containers", []) + template["spec"].get("initContainers", [])
            for container in containers:
                image = container["image"]
                item = published_workload_image(deployment, image, lock)
                if not item and deployment.config.get("_infrastructureOnly") is not True:
                    continue
                if item and image != item["reference"]:
                    raise Blocked("workload_image_pin_mismatch")
                matching = [pod for pod in rows if selector and all(pod["metadata"].get("labels", {}).get(k) == v for k, v in selector.items())
                            and not pod["metadata"].get("deletionTimestamp")]
                if not matching:
                    raise Blocked("published_workload_pods_missing")
                for pod in matching:
                    live = {c["name"]: c for field in ("containers", "initContainers") for c in pod["spec"].get(field, [])}
                    if live.get(container["name"], {}).get("image") != image:
                        raise Blocked("workload_image_pin_mismatch")
                    statuses = {c["name"]: c for field in ("containerStatuses", "initContainerStatuses") for c in pod.get("status", {}).get(field, [])}
                    actual = statuses.get(container["name"], {}).get("imageID", "")
                    digests = (item["digest"], item["platformDigest"]) if item else ((image.split("@", 1)[1],) if "@" in image else ())
                    if digests and not any(actual.endswith(value) for value in digests):
                        raise Blocked("workload_image_identity_mismatch")
                    if item:
                        count += 1
    if not count:
        raise Blocked("no_published_workload_images_observed")
    return count


def public_lock(lock):
    return {"sourceSHA": lock["sourceSHA"], "version": lock["version"], "registry": lock["registry"],
            "images": {name: {**{k: item[k] for k in ("reference", "digest", "platformDigest")},
                              "revisionVerified": item.get("revisionVerified", False), "versionVerified": item.get("versionVerified", False)}
                       for name, item in lock["images"].items()},
            "charts": {name: {k: item[k] for k in ("digest", "sha256")} for name, item in lock["charts"].items()}}


def run(args):
    work, output = args.work.resolve(), args.report.resolve()
    work.mkdir(parents=True, mode=0o700, exist_ok=False)
    summary = {"schemaVersion": 1, "startedAt": datetime.now(timezone.utc).isoformat(),
               "status": "RUNNING", "planOnly": not args.apply, "completionSubmissions": 0,
               "deploymentScope": "infrastructure-only",
               "stabilityWindowSeconds": 60,
               "stages": [], "notQualified": ["browser OAuth", "native runtime chat", "model inference",
                                                "Marketplace publication", "ADX ingestion", "provider background request count",
                                                "retained runtime qualification against the new Operator binary"]}
    stage = "inputs"
    deployment, baseline, observed = None, None, None
    allowed_additions = set()
    try:
        if not re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", args.release_tag):
            raise Blocked("invalid_release_tag")
        summary["releaseTag"] = args.release_tag
        private_write(output, json.dumps(summary, indent=2) + "\n")
        # Only config/kube inputs are taken from the environment. Remove them
        # before spawning tools so they never reach Helm or a credential plugin.
        config_env, kube_env = os.environ.pop("DEPLOY_CONFIG_YAML", ""), os.environ.pop("E2E_KUBE_CONFIG", "")
        config_text = config_env if args.config is None else args.config.read_text()
        kube_text = kube_env if args.kubeconfig is None else args.kubeconfig.read_text()
        summary["configSHA256"] = hashlib.sha256(config_text.encode()).hexdigest()
        config = credentials(work, config_text, kube_text)
        stage = "artifacts"
        lock = artifacts.resolve(args.source.resolve(), work / "artifacts", args.release_tag)
        summary["artifacts"] = public_lock(lock)
        private_write(work / "artifacts-lock.json", json.dumps(lock, indent=2))
        stage = "configuration"
        # The published mode renders and upgrades the same downloaded packages.
        with open(work / "diagnostics.log", "w", opener=lambda name, flags: os.open(name, flags, 0o600)) as log, redirect_stdout(log), redirect_stderr(log):
            config, values, roots = platform.infrastructure_configuration(config, lock, work / "charts")
            deployment = platform.Deployment(config, values, work / "deployment", published_artifacts=lock, chart_roots=roots)
            deployment.scratch.mkdir(mode=0o700)
            deployment.render()
            summary["renderedImageTypes"] = audit_rendered_images(deployment, lock)
            stage = "runtime_artifacts"
            summary["runtimeChartPins"] = verify_runtime_chart(roots, lock)
            stage = "baseline"
            # Verify context, server and cluster UID before recording any baseline.
            actual_uid = (deployment.resource("namespace", "kube-system") or {}).get("uid")
            if actual_uid != config["target"]["clusterUID"]:
                raise Blocked("cluster_uid_mismatch")
            baseline = capture(deployment)
            observed = copy.deepcopy(baseline)
            verify_planned_claim_ownership(deployment, baseline)
            private_write(work / "baseline.json", json.dumps(baseline, indent=2))
            summary["beforeReleases"] = baseline["releases"]
            summary["preservedResourceCount"] = len(baseline["objects"])
            reject_downgrade(baseline["releases"], lock["version"], managed_release_names(deployment))
            stage = "existing_agents"
            summary["runtimePolicy"] = "preserve-existing"
            verify_runtime_preservation(deployment, baseline, roots, lock)
            summary["runtimePreservation"] = "PASS"
            stage = "preflight"
            deployment.preflight()
            summary["changes"] = compare_manifests(deployment)
            planned_additions = {key: name for key, name in planned_claim_releases(deployment).items()
                                 if key not in baseline["objects"]}
            summary["plannedPVCAdditions"] = len(planned_additions)
            summary["entryBefore"] = verify_entry(config["domain"])
            summary["stages"].append({"stage": "preflight", "status": "PASS"})
            private_write(output, json.dumps(summary, indent=2) + "\n")
            if args.apply:
                stage = "deploy"
                current = capture(deployment)
                observe_preservation(baseline, observed, current)
                verify_runtime_preservation(deployment, current, roots, lock)
                if current["releases"] != baseline["releases"]:
                    raise Blocked("helm_revision_changed_before_deploy")
                private_write(work / "intent.json", json.dumps({"action": "deploy", "release": lock["version"], "before": baseline["releases"]}))
                expected_releases = copy.deepcopy(baseline["releases"])
                def before_upgrade(name, values=None):
                    nonlocal stage
                    stage = "upgrade_" + name
                    summary["stages"].append({"stage": stage, "status": "STARTED"})
                    private_write(output, json.dumps(summary, indent=2) + "\n")
                    private_write(work / "intent.json", json.dumps({"action": stage, "before": baseline["releases"]}))
                    if release_snapshot(deployment) != expected_releases:
                        raise Blocked("helm_revision_changed_before_upgrade")
                    current = capture(deployment)
                    observe_preservation(baseline, observed, current, allowed_additions=allowed_additions)
                    verify_runtime_preservation(deployment, current, roots, lock)
                    # The post-renderer repeats this check after Helm has
                    # rendered the chart, immediately before Helm writes its
                    # pending revision. This closes the check-then-act gap.
                    return int(expected_releases[name]["revision"])

                def after_upgrade(name, values=None):
                    expected_releases[name] = {"revision": str(int(expected_releases[name]["revision"]) + 1),
                                               "chart": platform.chart_package(name) + "-" + lock["version"], "status": "deployed"}
                    if release_snapshot(deployment) != expected_releases:
                        raise Blocked("helm_revision_changed_after_upgrade")
                    # Only the release that creates a claim can authorize its
                    # first appearance. A future platform ordinal appearing
                    # during Broker's upgrade is an unrelated claim, not ours.
                    allowed_additions.update(key for key, owner in planned_additions.items() if owner == name)
                    # Record newly created claims immediately, not only when
                    # the next release or final acceptance starts.
                    observe_preservation(baseline, observed, capture(deployment), allowed_additions=allowed_additions)
                    summary["expectedReleases"] = copy.deepcopy(expected_releases)
                    summary["stages"].append({"stage": stage, "status": "PASS"})
                    private_write(output, json.dumps(summary, indent=2) + "\n")
                summary["stages"].append({"stage": "deploy", "status": "STARTED"})
                private_write(output, json.dumps(summary, indent=2) + "\n")
                deployment.execute(before_upgrade=before_upgrade, after_upgrade=after_upgrade)
                summary["stages"].append({"stage": "deploy", "status": "PASS"})
                stage = "acceptance"
                after = stable_acceptance(deployment, baseline, expected_releases=expected_releases,
                                          allowed_additions=allowed_additions, observed=observed)
                verify_runtime_preservation(deployment, after, roots, lock)
                summary["afterReleases"] = after["releases"]
                summary["verifiedContainers"] = verify_images(deployment, lock)
                summary["entryAfter"] = verify_entry(config["domain"])
                summary["stages"].append({"stage": "acceptance", "status": "PASS"})
            else:
                after = capture(deployment)
                preservation(baseline, after)
                if after["releases"] != baseline["releases"]:
                    raise Blocked("helm_revision_changed_during_plan")
        summary["status"] = "PASS"
    except Exception as error:
        summary["status"] = "BLOCKED" if isinstance(error, Blocked) else "FAIL"
        summary["stages"].append({"stage": stage, "status": summary["status"],
                                  "code": str(error) if isinstance(error, Blocked) else "stage_failed"})
        if isinstance(error, artifacts.ArtifactError) and error.artifact:
            summary["artifactFailure"] = {"kind": error.artifact_kind, "name": error.artifact}
        private_write(work / "failure.txt", type(error).__name__ + ": " + str(error) + "\n")
        summary["recovery"] = "Stop; inspect the failed stage and Helm histories. No automatic retry or rollback."
        if deployment is not None and baseline is not None and args.apply:
            try:
                summary["afterReleases"] = release_snapshot(deployment, require_deployed=False)
                current = capture(deployment, require_deployed=False)
                observe_preservation(baseline, observed, current, allowed_additions=allowed_additions)
                summary["preservationAfterFailure"] = "PASS"
            except Exception:
                summary["preservationAfterFailure"] = "UNVERIFIED"
    finally:
        summary["finishedAt"] = datetime.now(timezone.utc).isoformat()
        private_write(output, json.dumps(summary, indent=2) + "\n")
        print(json.dumps(summary, indent=2))
    return 0 if summary["status"] == "PASS" else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path, help="clean checkout of the selected release tag (data only)")
    parser.add_argument("--release-tag", required=True)
    parser.add_argument("--work", required=True, type=Path, help="new private directory; never upload")
    parser.add_argument("--report", required=True, type=Path, help="sanitized JSON only")
    parser.add_argument("--config", type=Path, help="local alternative to DEPLOY_CONFIG_YAML")
    parser.add_argument("--kubeconfig", type=Path, help="local alternative to E2E_KUBE_CONFIG")
    parser.add_argument("--apply", action="store_true", help="perform writes after all gates; default is read-only")
    return run(parser.parse_args())


if __name__ == "__main__":
    sys.exit(main())
