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
               "istio", "broker", "platform", "sharedSkills", "skillMarketplace",
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
    for name in platform.CHARTS:
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
    return tuple(name for name in platform.CHARTS
                 if name != "istio" or istio.get("mode", "manage") == "manage")


def installation_claim_keys(deployment):
    """Return PVC identities referenced by the installed Helm manifests.

    Namespace membership alone is not an ownership boundary: unrelated
    workloads can legitimately use an installation namespace. StatefulSet
    claim-template prefixes are retained so existing ordinal claims remain in
    the preservation set even when a chart manifest contains only the template.
    """
    claims, prefixes = set(), []
    for name in platform.CHARTS:
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
    return claims, tuple(prefixes)


def planned_claim_keys(deployment):
    """Only claims explicitly created by managed rendered resources may appear.

    Volume references to external claims are not permission to create claims.
    StatefulSet additions use the rendered ordinal range, never a name prefix.
    """
    claims = set()
    for name in managed_release_names(deployment):
        namespace = deployment.config[name]["namespace"]
        for document in managed_documents(deployment.documents[name]):
            meta, spec = document.get("metadata", {}), document.get("spec", {})
            ns = meta.get("namespace", namespace)
            if document.get("kind") == "PersistentVolumeClaim":
                claims.add("pvc/" + ns + "/" + meta["name"])
            elif document.get("kind") == "StatefulSet":
                start = spec.get("ordinals", {}).get("start", 0)
                for claim in spec.get("volumeClaimTemplates", []):
                    for ordinal in range(start, start + spec.get("replicas", 1)):
                        claims.add(f"pvc/{ns}/{claim['metadata']['name']}-{meta['name']}-{ordinal}")
    return claims


def capture(deployment, *, require_deployed=True):
    """Read exact identities and spec hashes; do not save credentials or HOME."""
    snapshot = {"objects": {}, "releases": {}}
    runtime_namespaces = set()
    agent_uids = set()
    agent_namespaces = set()
    candidate_namespaces = {}
    for resource in PRESERVED:
        rows = json.loads(deployment.kubectl("get", resource, "--all-namespaces", "-o", "json"))["items"]
        for row in rows:
            meta, spec, status = row["metadata"], row.get("spec", {}), row.get("status", {})
            if resource == "agents.cetirun.microsoft.com" and status.get("namespace"):
                runtime_namespaces.add(status["namespace"])
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
                "suspended": spec.get("suspended", False),
            }
            if resource == "pvc":
                snapshot["objects"][key].update(
                    claimContractHash=digest({field: value for field, value in spec.items() if field != "volumeName"}),
                    volumeName=spec.get("volumeName", ""))
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
                agent_namespaces.add(meta.get("name"))
    # PVCs from unrelated workloads must not make an otherwise safe upgrade
    # fail. Retain only claims referenced by installed manifests and claims in
    # Agent-owned namespaces.
    installation_claims, claim_prefixes = installation_claim_keys(deployment)
    planned_claims = planned_claim_keys(deployment)
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
            snapshot["objects"]["pods/" + namespace + "/" + meta["name"]] = {
                "uid": meta["uid"], "specHash": digest(row["spec"]), "generation": meta.get("generation"),
                "deleting": bool(meta.get("deletionTimestamp")),
                "ready": bool(statuses) and all(item.get("ready") for item in statuses), "bound": None,
                "agentType": "", "image": "", "suspended": False,
            }
    snapshot["releases"] = release_snapshot(deployment, require_deployed=require_deployed)
    return snapshot


def agent_ready(row):
    meta, status = row.get("metadata", {}), row.get("status", {})
    return (status.get("observedGeneration") == meta.get("generation") and
            any(c.get("type") == "Ready" and c.get("status") == "True" for c in status.get("conditions", [])))


def registry_conflicts(snapshot, registry):
    return sum(1 for item in snapshot["objects"].values()
               if item["agentType"] in ("openclaw", "hermes") and
               (not item["image"] or not item["image"].startswith(registry + "/")))


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


def stable_acceptance(deployment, baseline, *, expected_releases=None, allowed_additions=frozenset(),
                      window=60, timeout=300, interval=15):
    """Observe several reconciles; never excuse UID/spec drift during settling."""
    start = time.monotonic()
    stable_since = None
    observed = copy.deepcopy(baseline)
    while time.monotonic() - start <= timeout:
        current = capture(deployment)
        for key in observed["objects"].keys() & current["objects"].keys() & set(allowed_additions):
            previous, latest = observed["objects"][key], current["objects"][key]
            if key not in baseline["objects"] and not previous["bound"]:
                # Kubernetes may bind a newly planned claim after Helm returns.
                # Permit only its initial volumeName assignment; preserve UID,
                # the remaining storage contract and any assigned volume.
                if (previous.get("claimContractHash") and
                        previous["claimContractHash"] == latest.get("claimContractHash") and
                        previous.get("volumeName", "") in ("", latest.get("volumeName", ""))):
                    previous["specHash"] = latest["specHash"]
                    previous["volumeName"] = latest.get("volumeName", "")
        preservation(observed, current, readiness=False, allowed_additions=allowed_additions)
        # Pin the first observed identity of a newly created planned claim too.
        # Its replacement or loss during the stability window is still drift.
        for key in current["objects"].keys() - observed["objects"].keys():
            observed["objects"][key] = copy.deepcopy(current["objects"][key])
        for key in set(allowed_additions) & current["objects"].keys():
            if current["objects"][key]["bound"]:
                observed["objects"][key]["bound"] = True
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
                        if name in lock["images"]:
                            if image != lock["images"][name]["reference"]:
                                raise Blocked("rendered_image_not_locked")
                            observed.add(name)
    required = {"cetirun-operator", "cetirun-portal", "cetirun-ai-gateway", "cetirun-agent-broker", "cetirun-connections-mcp"}
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
    for name in platform.CHARTS:
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
                basename = image.split("@")[0].rsplit("/", 1)[-1].split(":")[0]
                item = lock["images"].get(basename)
                if not item:
                    continue
                if image != item["reference"]:
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
                    if not any(actual.endswith(value) for value in (item["digest"], item["platformDigest"])):
                        raise Blocked("workload_image_identity_mismatch")
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
               "stabilityWindowSeconds": 60,
               "stages": [], "notQualified": ["browser OAuth", "native runtime chat", "model inference",
                                                "Marketplace publication", "ADX ingestion", "provider background request count"]}
    stage = "inputs"
    deployment, baseline = None, None
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
            config, values, roots = platform.published_configuration(config, lock, work / "charts")
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
            private_write(work / "baseline.json", json.dumps(baseline, indent=2))
            summary["beforeReleases"] = baseline["releases"]
            summary["preservedResourceCount"] = len(baseline["objects"])
            reject_downgrade(baseline["releases"], lock["version"], managed_release_names(deployment))
            stage = "existing_agents"
            conflicts = registry_conflicts(baseline, lock["registry"])
            summary["registryConflictCount"] = conflicts
            if conflicts:
                raise Blocked("existing_agent_registry_conflict")
            stage = "preflight"
            deployment.preflight()
            summary["changes"] = compare_manifests(deployment)
            allowed_additions = planned_claim_keys(deployment) - baseline["objects"].keys()
            summary["plannedPVCAdditions"] = len(allowed_additions)
            summary["entryBefore"] = verify_entry(config["domain"])
            summary["stages"].append({"stage": "preflight", "status": "PASS"})
            private_write(output, json.dumps(summary, indent=2) + "\n")
            if args.apply:
                stage = "deploy"
                current = capture(deployment)
                preservation(baseline, current)
                if current["releases"] != baseline["releases"]:
                    raise Blocked("helm_revision_changed_before_deploy")
                private_write(work / "intent.json", json.dumps({"action": "deploy", "release": lock["version"], "before": baseline["releases"]}))
                upgrade = deployment.upgrade
                expected_releases = copy.deepcopy(baseline["releases"])
                def recorded_upgrade(name, values=None):
                    nonlocal stage
                    stage = "upgrade_" + name
                    summary["stages"].append({"stage": stage, "status": "STARTED"})
                    private_write(output, json.dumps(summary, indent=2) + "\n")
                    private_write(work / "intent.json", json.dumps({"action": stage, "before": baseline["releases"]}))
                    if release_snapshot(deployment) != expected_releases:
                        raise Blocked("helm_revision_changed_before_upgrade")
                    # The post-renderer repeats this check after Helm has
                    # rendered the chart, immediately before Helm writes its
                    # pending revision. This closes the check-then-act gap.
                    deployment.expected_release_revision = int(expected_releases[name]["revision"])
                    try:
                        upgrade(name, values)
                    finally:
                        deployment.expected_release_revision = None
                    chart_names = {"istio": "cetirun-istio", "broker": "cetirun-agent-broker", "platform": "cetirun-platform"}
                    expected_releases[name] = {"revision": str(int(expected_releases[name]["revision"]) + 1),
                                               "chart": chart_names[name] + "-" + lock["version"], "status": "deployed"}
                    if release_snapshot(deployment) != expected_releases:
                        raise Blocked("helm_revision_changed_after_upgrade")
                    summary["expectedReleases"] = copy.deepcopy(expected_releases)
                    summary["stages"].append({"stage": stage, "status": "PASS"})
                    private_write(output, json.dumps(summary, indent=2) + "\n")
                deployment.upgrade = recorded_upgrade
                summary["stages"].append({"stage": "deploy", "status": "STARTED"})
                private_write(output, json.dumps(summary, indent=2) + "\n")
                deployment.execute()
                summary["stages"].append({"stage": "deploy", "status": "PASS"})
                stage = "acceptance"
                after = stable_acceptance(deployment, baseline, expected_releases=expected_releases,
                                          allowed_additions=allowed_additions)
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
                preservation(baseline, current, allowed_additions=allowed_additions)
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
