#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Install the default CetiRun platform across its existing Helm releases."""

import argparse
import base64
from collections import Counter
import copy
from datetime import datetime, timezone
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

import yaml


ROOT = Path(__file__).resolve().parents[1]
CLUSTER_KINDS = {"Namespace", "CustomResourceDefinition", "ClusterRole", "ClusterRoleBinding",
                 "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration",
                 "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding", "StorageClass", "PersistentVolume"}
CHARTS = {"istio": "infra/istio", "broker": "app/cetirun-agent-broker/chart",
          "platform": "app/cetirun-platform"}
AGENT_ACCOUNT_FIELDS = ("metadata.name", "spec.agent.type", "spec.team", "spec.sandboxRef",
                        "spec.helm.values.serviceAccount.name", "spec.helm.values.serviceAccount.role",
                        "spec.helm.values.serviceAccount.automountToken")
AGENT_POLICY_MARKERS = (
    '{{with .spec}}{{if .sandboxRef}}protected{{end}}'
    '{{with .modelAccess}}{{if .monthlyBudgetUSD}}protected{{end}}{{end}}{{end}}'
    '{{with .status}}{{with .aiGateway}}{{if .policyRevision}}protected{{end}}{{end}}'
    '{{range $key,$value := .}}{{if or (eq $key "policy") (eq $key "sandbox")}}protected{{end}}{{end}}'
    '{{range .conditions}}{{if eq .type "SandboxReady"}}protected{{end}}{{end}}{{end}}'
    '{{range .metadata.finalizers}}{{if or (eq . "cetirun.microsoft.com/sandbox-hold") '
    '(eq . "cetirun.microsoft.com/sandbox-preparation") (eq . "cetirun.microsoft.com/sandbox-bootstrap")}}protected{{end}}{{end}}'
    '{{range $key,$value := .metadata.annotations}}{{if or (eq $key "cetirun.microsoft.com/sandbox-authorization") '
    '(eq $key "cetirun.microsoft.com/sandbox-reauthorize")}}protected{{end}}{{end}}'
)
# The Operator reads customRuntime strictly and stops at startup on a field it does not know.
OPERATOR_VALUES = ROOT / "app/cetirun-operator/chart/values.yaml"
REMOVED_CUSTOM_FIELDS = {"agentManagedModel", "gatewayImages"}
# An error names a failed command by its tool, its operands and these options. Other option
# values can be templates or local files. Tool output can hold rendered Secrets, so it is not shown.
NAMED_OPTIONS = {"--namespace", "--name", "--timeout", "--version", "--dry-run"}
FLAG_OPTIONS = {"--install", "--ignore-not-found", "--reuse-values", "--skip-refresh", "--minify", "-noout",
                "--deployed", "--failed", "--pending", "--uninstalling", "--uninstalled", "--superseded"}
# A chart's own refusal during the offline render, from its fail or required call. Charts
# word these messages with value names, never with Secret material.
CHART_REFUSAL = re.compile(r"execution error at \(([^)\n]+)\): ([^\n]+)")


def step(command):
    """Name a command for an error message, without its output, local files or templates."""
    words = [str(word) for word in command]
    while words[0] == sys.executable or words[0].startswith("-"):
        words = words[1:]
    if words[1:2] in (["--context"], ["--kube-context"]):
        words = words[:1] + words[3:]
    shown, rest = [Path(words[0]).name], iter(words[1:])
    for word in rest:
        if word.split("=", 1)[0] in NAMED_OPTIONS:
            shown.append(word if "=" in word else f"{word} {next(rest, '')}".rstrip())
        elif word.startswith("-"):
            if "=" not in word and word not in FLAG_OPTIONS:
                next(rest, None)
        elif not Path(word).is_absolute():
            shown.append(word)
    return " ".join(shown)


def load_helper(filename):
    spec = importlib.util.spec_from_file_location(filename.replace("-", "_"), ROOT / "scripts" / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


MESH = load_helper("ensure-oauth2-proxy-id-token-header.py")
RELEASE = load_helper("cetirun-release.py")


def mapping(value, name):
    if not isinstance(value, dict):
        raise ValueError(f"{name} must be a mapping")
    return value


def required(data, path):
    value = get_value(data, path)
    if not isinstance(value, str) or not value.strip() or value != value.strip():
        raise ValueError(f"{path} is required and must be a nonempty string")
    return value


def get_value(data, path):
    value = data
    for key in path.split("."):
        value = value.get(key) if isinstance(value, dict) else None
    return value


def merge(base, update):
    result = copy.deepcopy(base)
    for key, value in mapping(update, "values").items():
        result[key] = merge(result[key], value) if isinstance(value, dict) and isinstance(result.get(key), dict) else copy.deepcopy(value)
    return result


def set_value(data, path, value):
    keys = path.split(".")
    for key in keys[:-1]:
        data = data.setdefault(key, {})
        mapping(data, path)
    data[keys[-1]] = value


def walk(value):
    if isinstance(value, dict):
        yield value
        for item in value.values():
            yield from walk(item)
    elif isinstance(value, list):
        for item in value:
            yield from walk(item)


def yaml_file(path):
    return mapping(yaml.load(path.read_text(), Loader=MESH.UniqueKeyLoader), str(path))


def configuration(path):
    config = yaml_file(path)
    for field in ("target.context", "target.cluster", "target.server", "target.clusterUID",
                  "release.registry", "release.version", "release.gitSHA", "domain", "identityRealm", "ownerAlias",
                  "istio.namespace", "istio.release", "istio.revision", "istio.controlPlaneNamespace",
                  "istio.controlPlaneDeployment", "istio.ingressNamespace", "istio.ingressDeployment",
                  "istio.ingressServiceAccount", "istio.meshConfigMap", "broker.namespace", "broker.release",
                  "platform.namespace", "platform.release"):
        required(config, field)
    RELEASE.coordinates(argparse.Namespace(registry=config["release"]["registry"], version=config["release"]["version"],
                                           git_sha=config["release"]["gitSHA"], chart_namespace="helm"))
    if not re.fullmatch(r"https://[^\s/?#@]+", config["target"]["server"]):
        raise ValueError("target.server must be an HTTPS Kubernetes API origin without embedded credentials")
    if not re.fullmatch(r"[a-z0-9][a-z0-9.-]*\.[a-z0-9-]+", config["domain"]):
        raise ValueError("domain must be a hostname, without a scheme or path")
    for name in CHARTS:
        for field in ("namespace", "release"):
            if not re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", config[name][field]):
                raise ValueError(f"{name}.{field} must be a DNS label")
    if len({config[name]["namespace"] for name in CHARTS}) != 3:
        raise ValueError("Istio, Broker credentials, and platform/MCP require separate namespaces")
    if config["istio"].get("mode", "manage") not in ("manage", "external"):
        raise ValueError("istio.mode must be manage or external")
    if config["istio"].get("tlsMode", "external") not in ("external", "retain"):
        raise ValueError("istio.tlsMode must be external or retain for an existing chart-owned certificate")
    if config.get("instances"):
        raise ValueError("Instance creation and CetiAdmin ServiceAccount/token coexistence are not part of this deployment; existing Agents and HOME are preserved")
    for field in ("tenantId", "portalClientId", "blueprintClientId", "authorizationClientId", "encryptionClientId", "keyVaultKeyUrl"):
        required(config, f"identity.{field}")
    for field in ("customRuntime.storageClass", "customRuntime.storageSize", "customRuntime.accessMode"):
        required(config, f"platform.values.cetirun-operator.{field}")
    for field in ("storageClass", "size"):
        required(config, f"platform.values.cetirun-ai-gateway.postgresql.bundled.persistence.{field}")
    raw_custom = get_value(config, "platform.values.cetirun-operator.customRuntime")
    if type(raw_custom.get("fsGroup")) is not int or not 1 <= raw_custom["fsGroup"] <= 2147483647:
        raise ValueError("customRuntime.fsGroup must explicitly name the qualified non-root storage group")
    unknown = sorted(set(raw_custom) - set(yaml_file(OPERATOR_VALUES)["customRuntime"]))
    if unknown:
        note = " Every approved Custom image uses the AI Gateway binding." if REMOVED_CUSTOM_FIELDS & set(unknown) else ""
        raise ValueError("Remove " + ", ".join(f"customRuntime.{field}" for field in unknown) + " from the environment file: "
                         "the Operator stops at startup on a customRuntime field that it does not know." + note)

    values = {name: merge(yaml_file(ROOT / chart / "values.yaml"), config[name].get("values", {}))
              for name, chart in CHARTS.items()}
    platform = values["platform"]
    for component in ("operator", "portal", "ai-gateway", "connections-mcp"):
        key = f"cetirun-{component}"
        platform[key] = merge(yaml_file(ROOT / f"app/{key}/chart/values.yaml"), platform.get(key, {}))
    pns, bns, ins = (config[name]["namespace"] for name in ("platform", "broker", "istio"))
    identity, registry, version = config["identity"], config["release"]["registry"], config["release"]["version"]
    domain, revision = config["domain"], config["istio"]["revision"]
    gateway = f"{config['istio']['ingressNamespace']}/cetirun-gateway"
    broker_http = f"http://cetirun-agent-broker.{bns}.svc.cluster.local:8080"
    assignments = {
        "istio": {"domain": domain, "clientId": identity["portalClientId"], "tenantId": identity["tenantId"],
                  "gateway.namespace": config["istio"]["ingressNamespace"],
                  "egress.namespace": config["istio"]["controlPlaneNamespace"],
                  "portalAuth.clusterHost": f"cetirun-portal.{pns}.svc.cluster.local"},
        "broker": {"image.repository": f"{registry}/cetirun-agent-broker", "image.tag": version,
                   "tls.enabled": True, "istio.sidecarInjection": True, "istio.revision": revision,
                   "portal.namespace": pns, "operator.namespace": pns, "toolService.namespace": pns,
                   "agentIdentity.portalOrigin": f"https://{domain}", "agentIdentity.portalBaseUrl": f"https://{domain}",
                   "agentIdentity.redirectUri": f"https://{domain}/api/v1/connections/callback"},
        "platform": {"global.registry": registry, "global.version": version,
                     "global.portalChat.enabled": True, "global.portalChat.identityRealm": config["identityRealm"],
                     "global.memoryContext.image": f"{registry}/cetirun-memory-context:{version}",
                     "istio.enabled": True, "istio.oauth2Proxy.namespace": ins,
                     "cetirun-operator.namespace": pns, "cetirun-operator.domainSuffix": domain,
                     "cetirun-operator.istioRevision": revision, "cetirun-operator.istioGateway": gateway,
                     "cetirun-operator.cetirun.helmValues.networkPolicy.istioIngressNamespace": config["istio"]["ingressNamespace"],
                     "cetirun-operator.customRuntime.enabled": True, "cetirun-operator.memoryContext.enabled": True,
                     "cetirun-operator.sandboxAdmission.enabled": True, "cetirun-operator.sandboxAdmission.enforce": True,
                     "cetirun-operator.connectionsMCP.url": f"https://cetirun-connections-mcp.{pns}.svc.cluster.local/mcp",
                     "cetirun-operator.connectionsMCP.brokerNamespace": bns,
                     "cetirun-operator.connectionsMCP.brokerServiceAccount": values["broker"]["serviceAccount"]["name"] or "cetirun-agent-broker",
                     "cetirun-operator.agentIdentity.enabled": True, "cetirun-operator.agentIdentity.brokerURL": broker_http,
                     "cetirun-operator.agentIdentity.tenantId": identity["tenantId"],
                     "cetirun-operator.agentIdentity.blueprintClientId": identity["blueprintClientId"],
                     "cetirun-portal.istio.host": domain, "cetirun-portal.istio.gateway": gateway,
                     "cetirun-portal.istio.gatewayNamespace": config["istio"]["ingressNamespace"],
                     "cetirun-portal.istio.gatewayServiceAccount": config["istio"]["ingressServiceAccount"],
                     "cetirun-portal.aiGateway.catalogNamespace": pns,
                     "cetirun-portal.features.agentToolsEnabled": True, "cetirun-portal.features.agentIdentityEnabled": True,
                     "cetirun-portal.features.allowedAgentTypes": ["hermes", "openclaw", "custom"],
                     "cetirun-portal.agentIdentity.tenantId": identity["tenantId"],
                     "cetirun-portal.agentIdentity.portalClientId": identity["portalClientId"],
                     "cetirun-portal.agentIdentity.brokerURL": broker_http, "cetirun-portal.agentTools.brokerURL": broker_http,
                     "cetirun-connections-mcp.enabled": True,
                     "cetirun-connections-mcp.image.repository": f"{registry}/cetirun-connections-mcp",
                     "cetirun-connections-mcp.image.tag": version,
                     "cetirun-connections-mcp.broker.baseURL": f"https://cetirun-agent-broker.{bns}.svc.cluster.local",
                     "cetirun-connections-mcp.istio.brokerNamespace": bns,
                     "cetirun-connections-mcp.istio.controlPlaneNamespace": config["istio"]["controlPlaneNamespace"],
                     "cetirun-connections-mcp.istio.sidecarInjection": True, "cetirun-connections-mcp.istio.revision": revision,
                     "cetirun-connections-mcp.portal.baseURL": f"https://{domain}"},
    }
    for field in ("tenantId", "blueprintClientId", "authorizationClientId", "encryptionClientId", "keyVaultKeyUrl"):
        assignments["broker"][f"agentIdentity.{field}"] = identity[field]
    for name, fields in assignments.items():
        for field, value in fields.items():
            supplied = get_value(config[name].get("values", {}), field)
            if field == "global.memoryContext.image" and supplied:
                value = supplied
            elif supplied is not None and supplied != value:
                raise ValueError(f"{name}.values.{field} conflicts with the complete deployment inputs")
            set_value(values[name], field, value)

    operator, portal, ai, mcp = (platform[f"cetirun-{name}"] for name in ("operator", "portal", "ai-gateway", "connections-mcp"))
    for valueset, standard in ((values["broker"], "cetirun-agent-broker"), (ai, "cetirun-ai-gateway"), (mcp, "cetirun-connections-mcp")):
        if valueset.get("fullnameOverride") not in (None, "", standard):
            raise ValueError(f"The complete deployment requires the {standard} Service name")
    values["broker"]["portal"]["serviceAccount"] = portal["serviceAccount"]["name"]
    values["broker"]["operator"]["serviceAccount"] = operator["serviceAccount"]["name"]
    values["broker"]["toolService"]["serviceAccount"] = mcp["serviceAccount"]["name"]
    operator["sandboxAdmission"]["portalServiceAccount"] = portal["serviceAccount"]["name"]
    if len({operator["connectionsMCP"]["audience"], mcp["workloadTokenAudience"], values["broker"]["workloadTokenAudience"]}) != 1:
        raise ValueError("Operator, MCP and Broker workload token audiences must match")
    runtime_lists = (operator["connectionsMCP"]["runtimeTypes"], portal["agentTools"]["runtimeTypes"], values["broker"]["toolService"]["runtimeTypes"])
    for runtimes in runtime_lists:
        if not isinstance(runtimes, list) or not runtimes or any(runtime not in ("hermes", "openclaw") for runtime in runtimes) or len(runtimes) != len(set(runtimes)):
            raise ValueError("MCP runtimeTypes must be nonempty lists of distinct supported runtimes")
    if any(set(runtimes) != set(runtime_lists[0]) for runtimes in runtime_lists[1:]):
        raise ValueError("Operator connectionsMCP.runtimeTypes, Portal agentTools.runtimeTypes and Broker toolService.runtimeTypes must match")
    portal["admins"]["aliases"] = sorted(set([*portal["admins"]["aliases"], config["ownerAlias"]]))
    if values["istio"]["oauth2Proxy"]["mode"] != "entra-id":
        raise ValueError("The complete Agent Identity deployment requires Entra ID; local-oidc cannot supply Broker identity")
    required(values["istio"], "allowedGroups")
    if values["istio"].get("cookieSecret") or values["istio"]["tls"].get("enabled") or values["istio"]["tls"].get("key"):
        raise ValueError("Use existing Gateway TLS and cookie Secrets, not inline cookieSecret or tls material")
    if bns == platform["global"]["registryCredentialNamespace"]:
        raise ValueError("Registry credentials must not share the Broker credential namespace")
    for component in ("operator", "portal", "ai-gateway", "connections-mcp"):
        if platform[f"cetirun-{component}"].get("enabled", True) is not True:
            raise ValueError(f"The default deployment requires cetirun-{component}.enabled=true")
    if not values["broker"]["identityModes"]["microsoftDelegated"]["enabled"]:
        raise ValueError("The default deployment requires the Broker Auth SDK")
    required(values["broker"], "tls.secretName")
    for field in ("tls.secretName", "broker.clientTLSSecretName"):
        required(mcp, field)
    for field in ("sandboxAdmission.tlsSecretName", "sandboxAdmission.caBundle", "connectionsMCP.caSecretName", "azure.clientId"):
        required(operator, field)
    if not operator["sandboxAdmission"]["adminUsers"] and not operator["sandboxAdmission"]["adminGroups"]:
        raise ValueError("Explicit Kubernetes admission administrators are required")
    custom = operator["customRuntime"]
    if not custom["approvedImages"] or any(not re.fullmatch(r"\S+@sha256:[0-9a-f]{64}", image) for image in custom["approvedImages"]):
        raise ValueError("Custom runtime requires explicitly qualified approvedImages pinned by digest")
    skills = mapping(config.get("sharedSkills"), "sharedSkills")
    for field in ("storageClassName", "resourceGroup", "storageAccount", "containerName"):
        required(skills, field)
    operator["cetirun"]["helmValues"]["skills"] = {**skills, "shared": True}
    for field in ("providerCredentials.existingSecret", "auth.masterKey.existingSecret", "auth.saltKey.existingSecret",
                  "auth.adminUI.passwordSecret.existingSecret", "redis.passwordSecret.existingSecret"):
        required(ai, field)
    if ai["providerCredentials"].get("inline"):
        raise ValueError("Provider credentials must use an existing Secret")
    operator["aiGateway"]["masterKeySecret"] = {"name": ai["auth"]["masterKey"]["existingSecret"], "key": ai["auth"]["masterKey"]["key"]}
    if not ai["postgresql"]["bundled"]["enabled"] or not ai["redis"]["bundled"]["enabled"]:
        raise ValueError("This deployment manages bundled PostgreSQL and Valkey; external database migration needs a separate plan")
    models = {entry.get("model_name"): entry for entry in ai["models"]}
    auto = models.get("auto", {}).get("litellm_params", {})
    decision = models.get("decision-1", {})
    router = auto.get("complexity_router_config", {})
    if (auto.get("model") != "auto_router/complexity_router" or router.get("classifier_type") != "jev"
            or router.get("jev_classifier_config", {}).get("deployment_name") != "decision-1"):
        raise ValueError("models must configure auto with the existing jev classifier and logical decision-1 deployment")
    if (not decision.get("litellm_params", {}).get("model", "").startswith("azure_ai/")
            or decision.get("model_info", {}).get("base_model") != "azure_ai/Microsoft-Decision-1"
            or decision.get("model_info", {}).get("mode") != "evaluation"):
        raise ValueError("decision-1 needs an explicit azure_ai/<deployment>, base_model azure_ai/Microsoft-Decision-1 and evaluation mode. "
                         "LiteLLM prices the model only by that name, and the Operator does not offer an unpriced classifier to auto")
    decision_params = decision["litellm_params"]
    if not decision_params["model"].removeprefix("azure_ai/"):
        raise ValueError("decision-1 requires a real Azure deployment name")
    for field in ("api_base", "api_key"):
        required(decision_params, field)
    if "api_version" in decision_params:
        required(decision_params, "api_version")
    if set(router.get("tiers", {})) != {"SIMPLE", "MEDIUM", "COMPLEX", "REASONING"}:
        raise ValueError("auto requires explicit SIMPLE, MEDIUM, COMPLEX and REASONING tiers")
    for entry in ai["models"]:
        params = entry.get("litellm_params", {})
        if "api_key" in params and not re.fullmatch(r"os\.environ/[A-Za-z_][A-Za-z0-9_]*", str(params["api_key"])):
            raise ValueError("Model api_key must reference a provider Secret environment variable")
    return config, values


class Deployment:
    def __init__(self, config, values, scratch):
        self.config, self.values, self.scratch = config, values, scratch
        self.documents, self.files, self.charts = {}, {}, {}
        self.existing_releases, self.namespaces = set(), {}
        self.mesh_uid = None

    def run(self, command, *, env=None, input_text=None):
        result = subprocess.run(command, cwd=ROOT, env=env, input=input_text, text=True, capture_output=True)
        if result.returncode:
            # Name the step, but do not echo Helm-rendered Secrets or external tool response bodies.
            # Only the offline render reports the chart's own refusal.
            refusals = CHART_REFUSAL.findall(result.stderr or "") if list(map(str, command[:2])) == ["helm", "template"] else []
            reason = "".join(f"; {template}: {message.strip()}" for template, message in refusals)
            raise ValueError(f"{step(command)} failed with exit {result.returncode}{reason}; deployment did not complete")
        return result.stdout

    def kubectl(self, *args, input_text=None):
        return self.run(["kubectl", "--context", self.config["target"]["context"], "--request-timeout=15s", *args], input_text=input_text)

    def helm(self, *args):
        return self.run(["helm", "--kube-context", self.config["target"]["context"], *args])

    def resource(self, kind, name, namespace=""):
        args = ["get", kind, name, "--ignore-not-found", "-o", "jsonpath={.metadata}"]
        if namespace:
            args += ["--namespace", namespace]
        raw = self.kubectl(*args)
        return json.loads(raw) if raw.strip() else None

    def render(self):
        for entry in RELEASE.inventory()["charts"]:
            source = ROOT / entry["path"]
            shutil.copytree(source, self.scratch / entry["path"], ignore=shutil.ignore_patterns("charts", "__pycache__"))
        offline = {**os.environ, "KUBECONFIG": os.devnull, "HELM_REGISTRY_CONFIG": os.devnull}
        self.run(["helm", "dependency", "build", "--skip-refresh", str(self.scratch / CHARTS["platform"])], env=offline)
        for name, relative in CHARTS.items():
            self.charts[name] = self.scratch / relative
            self.files[name] = self.scratch / f"{name}-values.json"
            actual_values = copy.deepcopy(self.values[name])
            if name == "istio" and self.config["istio"].get("tlsMode") == "retain":
                del actual_values["tls"]
            self.files[name].write_text(json.dumps(actual_values))
            render_values = self.files[name]
            if name == "istio":
                synthetic = {**self.values[name], "cookieSecret": "offline-render-only-not-a-real-key"}
                if self.config["istio"].get("tlsMode") == "retain":
                    synthetic["tls"] = {"enabled": True, "cert": "offline-render-only", "key": "offline-render-only"}
                render_values = self.scratch / "istio-render-only.json"
                render_values.write_text(json.dumps(synthetic))
            section = self.config[name]
            raw = self.run(["helm", "template", section["release"], str(self.charts[name]), "--namespace", section["namespace"],
                            "--kube-version", "1.37.0", "--values", str(render_values)], env=offline)
            self.documents[name] = [doc for doc in yaml.safe_load_all(raw) if doc]
        # Render the bootstrap too, before any cluster writes.
        bootstrap = copy.deepcopy(self.values["platform"])
        admission = bootstrap["cetirun-operator"]["sandboxAdmission"]
        admission.update(enforce=False, coordinationConfigMapName="", coordinationConfigMapUID="")
        bootstrap["cetirun-portal"]["configurationCoordination"] = {"namespace": "", "name": "", "uid": ""}
        bootstrap["cetirun-operator"]["sandboxSources"].update(enabled=False, nativeExecution=False, plannedMigration=False,
            bootstrapOwnedPrerequisites=False, skillsCatalogConfigMapName="", placementConfigMapName="")
        bootstrap["cetirun-operator"]["connectionsMCP"]["nativeEnabled"] = False
        bootstrap["cetirun-portal"]["sandboxRuntimeDefaultsFromController"] = False
        self.files["bootstrap"] = self.scratch / "bootstrap-values.json"
        self.files["bootstrap"].write_text(json.dumps(bootstrap))
        self.run(["helm", "template", self.config["platform"]["release"], str(self.charts["platform"]),
                  "--namespace", self.config["platform"]["namespace"], "--kube-version", "1.37.0",
                  "--values", str(self.files["bootstrap"])], env=offline)

    def references(self):
        refs = set()
        for name, documents in self.documents.items():
            generated = {(doc["kind"], doc.get("metadata", {}).get("namespace", self.config[name]["namespace"]), doc["metadata"]["name"]) for doc in documents}
            for doc in documents:
                namespace = doc.get("metadata", {}).get("namespace", self.config[name]["namespace"])
                for item in walk(doc):
                    for field in ("secretKeyRef", "secretRef", "configMapKeyRef", "configMapRef"):
                        if field in item:
                            kind = "Secret" if field.startswith("secret") else "ConfigMap"
                            reference = item[field]
                            if (kind, namespace, reference["name"]) not in generated:
                                refs.add((kind, namespace, reference["name"], reference.get("key", "")))
                    if "secretName" in item:
                        if ("Secret", namespace, item["secretName"]) not in generated:
                            refs.add(("Secret", namespace, item["secretName"], ""))
                    for secret in item.get("imagePullSecrets", []):
                        refs.add(("Secret", namespace, secret["name"], ""))
        c, p = self.config, self.values["platform"]
        operator, mcp, broker = p["cetirun-operator"], p["cetirun-connections-mcp"], self.values["broker"]
        for namespace, name, keys in (
            (c["istio"]["ingressNamespace"], "cetirun-tls", ("tls.crt", "tls.key")),
            (c["broker"]["namespace"], broker["tls"]["secretName"], (broker["tls"]["certKey"], broker["tls"]["keyKey"], broker["tls"]["clientCAKey"])),
            (c["platform"]["namespace"], operator["sandboxAdmission"]["tlsSecretName"], ("tls.crt", "tls.key")),
            (c["platform"]["namespace"], operator["connectionsMCP"]["caSecretName"], ("ca.crt",)),
            (c["platform"]["namespace"], mcp["tls"]["secretName"], ("tls.crt", "tls.key")),
            (c["platform"]["namespace"], mcp["broker"]["clientTLSSecretName"], ("tls.crt", "tls.key", "ca.crt")),
        ):
            for key in keys:
                refs.add(("Secret", namespace, name, key))
        for field in ("platformConfigMapName", "skillsCatalogConfigMapName", "placementConfigMapName"):
            if operator["sandboxSources"].get(field):
                refs.add(("ConfigMap", c["platform"]["namespace"], operator["sandboxSources"][field], ""))
        for field in ("coordinationConfigMapName", "placementConfigMapName"):
            if operator["sandboxAdmission"].get(field):
                refs.add(("ConfigMap", c["platform"]["namespace"], operator["sandboxAdmission"][field], ""))
        ai = p["cetirun-ai-gateway"]
        for entry in ai["models"]:
            for value in entry.get("litellm_params", {}).values():
                if isinstance(value, str) and value.startswith("os.environ/"):
                    refs.add(("Secret", c["platform"]["namespace"], ai["providerCredentials"]["existingSecret"], value.removeprefix("os.environ/")))
        for storage in (operator["customRuntime"]["storageClass"], c["sharedSkills"]["storageClassName"],
                        ai["postgresql"]["bundled"]["persistence"]["storageClass"]):
            refs.add(("StorageClass", "", storage, ""))
        return sorted(refs)

    def secret_keys(self, namespace, name):
        output = self.kubectl("get", "secret", name, "--namespace", namespace, "-o",
                              'go-template={{range $key,$value := .data}}{{$key}}{{"\\n"}}{{end}}')
        return set(output.splitlines())

    def public_certificate(self, namespace, name, key):
        # JSONPath limits extraction, not the Kubernetes Secret GET or its RBAC.
        field = key.replace(".", "\\.")
        encoded = self.kubectl("get", "secret", name, "--namespace", namespace, "-o", f"jsonpath={{.data.{field}}}")
        try:
            return base64.b64decode(encoded, validate=True).decode("ascii")
        except (ValueError, UnicodeError) as error:
            raise ValueError(f"Invalid public certificate field {namespace}/{name}:{key}") from error

    def verify_certificate(self, label, certificate, ca, *, hostname=None, client_uri=None):
        leaf_file, ca_file = self.scratch / f"{label}-public.pem", self.scratch / f"{label}-ca.pem"
        leaf_file.write_text(certificate)
        ca_file.write_text(ca)
        command = ["openssl", "verify", "-CAfile", str(ca_file), "-untrusted", str(leaf_file),
                   "-purpose", "sslclient" if client_uri else "sslserver"]
        if hostname:
            command += ["-verify_hostname", hostname]
        self.run([*command, str(leaf_file)])
        if client_uri:
            extensions = self.run(["openssl", "x509", "-in", str(leaf_file), "-noout", "-ext", "subjectAltName"])
            if re.findall(r"\bURI:([^,\s]+)", extensions) != [client_uri]:
                raise ValueError("The Broker client certificate must carry exactly the configured MCP service URI")

    def check_tls(self):
        if shutil.which("openssl") is None:
            raise ValueError("openssl is required for certificate preflight; install it separately")
        c, p, b = self.config, self.values["platform"], self.values["broker"]
        ns, bns = c["platform"]["namespace"], c["broker"]["namespace"]
        operator, mcp = p["cetirun-operator"], p["cetirun-connections-mcp"]
        admission = operator["sandboxAdmission"]
        admission_ca = base64.b64decode(admission["caBundle"], validate=True).decode("ascii")
        self.verify_certificate("admission", self.public_certificate(ns, admission["tlsSecretName"], "tls.crt"), admission_ca,
                                hostname=f"cetirun-sandbox-admission.{ns}.svc")
        self.verify_certificate("mcp", self.public_certificate(ns, mcp["tls"]["secretName"], "tls.crt"),
                                self.public_certificate(ns, operator["connectionsMCP"]["caSecretName"], "ca.crt"),
                                hostname=f"cetirun-connections-mcp.{ns}.svc.cluster.local")
        self.verify_certificate("broker", self.public_certificate(bns, b["tls"]["secretName"], b["tls"]["certKey"]),
                                self.public_certificate(ns, mcp["broker"]["clientTLSSecretName"], "ca.crt"),
                                hostname=f"cetirun-agent-broker.{bns}.svc.cluster.local")
        self.verify_certificate("broker-client", self.public_certificate(ns, mcp["broker"]["clientTLSSecretName"], "tls.crt"),
                                self.public_certificate(bns, b["tls"]["secretName"], b["tls"]["clientCAKey"]),
                                client_uri=f"spiffe://cluster.local/ns/{b['toolService']['namespace']}/sa/{b['toolService']['serviceAccount']}")
        ingress = self.scratch / "ingress-public.pem"
        ingress.write_text(self.public_certificate(c["istio"]["ingressNamespace"], "cetirun-tls", "tls.crt"))
        self.run(["openssl", "x509", "-in", str(ingress), "-noout", "-checkend", "0"])
        checked = self.run(["openssl", "x509", "-in", str(ingress), "-noout", "-checkhost", c["domain"]])
        if f"Hostname {c['domain']} does match certificate" not in checked.splitlines():
            raise ValueError("The ingress certificate does not cover the configured domain")
        starts = self.run(["openssl", "x509", "-in", str(ingress), "-noout", "-startdate"])
        not_before = datetime.strptime(starts.strip().removeprefix("notBefore="), "%b %d %H:%M:%S %Y %Z").replace(tzinfo=timezone.utc)
        if not_before > datetime.now(timezone.utc):
            raise ValueError("The ingress certificate is not yet valid")

    def check_agent_mcp_conflicts(self):
        if not self.resource("CustomResourceDefinition", "agents.cetirun.microsoft.com"):
            return
        fields = '{"\\t"}'.join("{." + field + "}" for field in AGENT_ACCOUNT_FIELDS)
        output = self.kubectl("get", "agents.cetirun.microsoft.com", "-o", 'jsonpath={range .items[*]}' + fields + '{"\\n"}{end}')
        operator = self.values["platform"]["cetirun-operator"]
        defaults = operator["cetirun"]["helmValues"].get("serviceAccount", {})
        team_policy = {}
        for row in output.splitlines():
            columns = row.split("\t")
            if len(columns) != len(AGENT_ACCOUNT_FIELDS):
                raise ValueError("Cannot inspect existing Agent ServiceAccount settings safely")
            name, runtime, team, sandbox, account, role, token = columns
            if (runtime or "openclaw") not in operator["connectionsMCP"]["runtimeTypes"] or sandbox:
                continue
            account, role = account or defaults.get("name", ""), role or defaults.get("role", "")
            token_required = token.lower() == "true" if token else defaults.get("automountToken") is True
            if not (account or role or token_required):
                continue
            if self.kubectl("get", "agent", name, "-o", "go-template=" + AGENT_POLICY_MARKERS).strip():
                continue
            if team:
                if team not in team_policy:
                    team_policy[team] = self.kubectl("get", "team", team, "-o",
                        'go-template={{with .spec}}{{if or .agentPolicyRef .sandboxRefs .defaultSandboxRef}}protected{{end}}{{end}}').strip()
                if team_policy[team]:
                    continue
            raise ValueError(f"Agent {name}: MCP would replace its ServiceAccount/API token settings; CetiAdmin ServiceAccount/token coexistence needs a separate approved change")

    def current_control(self, name, container, environment):
        namespace = self.config["platform"]["namespace"]
        if self.resource("Deployment", name, namespace) is None:
            return None
        prefix = '.spec.template.spec.containers[?(@.name=="' + container + '")]'
        args = self.kubectl("get", "deployment", name, "--namespace", namespace, "-o", "jsonpath={" + prefix + ".args}") if container == "admission" else "[]"
        values = {}
        for field in environment:
            encoded = self.kubectl("get", "deployment", name, "--namespace", namespace, "-o",
                                  "jsonpath-as-json={" + prefix + '.env[?(@.name=="' + field + '")]}')
            entries = json.loads(encoded or "[]")
            if not isinstance(entries, list) or len(entries) > 1 or any("valueFrom" in item or not isinstance(item.get("value"), str) for item in entries):
                raise ValueError(f"Cannot inspect existing {field} control value; external references need a separate approved procedure")
            values[field] = entries[0]["value"] if entries else ""
        try:
            arguments = json.loads(args or "[]")
        except ValueError as error:
            raise ValueError(f"Cannot inspect existing {name} control arguments") from error
        if not isinstance(arguments, list) or not all(isinstance(arg, str) for arg in arguments):
            raise ValueError(f"Cannot inspect existing {name} control arguments")
        return {"args": arguments, "env": values}

    def rendered_control(self, name, container):
        deployment = next(doc for doc in self.documents["platform"] if doc["kind"] == "Deployment" and doc["metadata"]["name"] == name)
        spec = next(item for item in deployment["spec"]["template"]["spec"]["containers"] if item["name"] == container)
        return {"args": spec.get("args", []), "env": {item["name"]: item.get("value", "") for item in spec.get("env", [])}}

    @staticmethod
    def control_json(control, field, *, required=False):
        encoded = (control or {}).get("env", {}).get(field, "")
        if not encoded:
            if required:
                raise ValueError("Cannot inspect existing admission configuration; preserve its effective protection before upgrading")
            return {}
        try:
            return mapping(json.loads(encoded), field)
        except (ValueError, TypeError) as error:
            raise ValueError(f"Cannot inspect existing {field} protection configuration") from error

    def check_existing_controls(self):
        platform, operator = self.values["platform"], self.values["platform"]["cetirun-operator"]
        admission = operator["sandboxAdmission"]
        has_webhook, has_pod_rules = False, False
        for kind, name in (("ValidatingWebhookConfiguration", "cetirun-sandbox-admission"),
                           ("ValidatingWebhookConfiguration", "cetirun-sandbox-pods"),
                           ("ValidatingWebhookConfiguration", "cetirun-sandbox-placement"),
                           ("MutatingWebhookConfiguration", "cetirun-sandbox-admission")):
            metadata = self.resource(kind, name)
            if metadata is None:
                continue
            owner = metadata.get("annotations", {})
            if (owner.get("meta.helm.sh/release-name"), owner.get("meta.helm.sh/release-namespace")) != (self.config["platform"]["release"], self.config["platform"]["namespace"]):
                raise ValueError(f"Refusing to modify admission protection owned by another release: {name}")
            has_webhook = True
            webhooks = json.loads(self.kubectl("get", kind, name, "-o", "jsonpath={.webhooks}"))
            if not isinstance(webhooks, list) or not webhooks:
                raise ValueError(f"Cannot inspect existing admission protection: {name}")
            for webhook in webhooks:
                for rule in webhook.get("rules", []):
                    if any(group in ("", "*") for group in rule.get("apiGroups", [])):
                        has_pod_rules |= any(resource in ("pods", "bindings", "*") or resource.startswith("pods/") for resource in rule.get("resources", []))
        if has_pod_rules and not admission["podsEnabled"]:
            raise ValueError("Preserve existing Pod admission protection with sandboxAdmission.podsEnabled=true")
        old_admission = self.current_control("cetirun-sandbox-admission", "admission", ("CETIRUN_SANDBOX_ADMISSION",))
        if has_webhook and old_admission is None:
            raise ValueError("Cannot inspect existing admission configuration behind the installed webhooks")
        self.enforced = has_webhook or old_admission is not None
        new_admission = self.rendered_control("cetirun-sandbox-admission", "admission")
        if old_admission is not None:
            old = self.control_json(old_admission, "CETIRUN_SANDBOX_ADMISSION", required=True)
            new = self.control_json(new_admission, "CETIRUN_SANDBOX_ADMISSION", required=True)
            for field in ("adminUsers", "adminGroups"):
                if set(old.get(field) or []) != set(new.get(field) or []):
                    raise ValueError(f"Preserve existing admission authority field {field}; changes need a separate approved procedure")
            if old.get("controllerUsername") != new.get("controllerUsername"):
                raise ValueError("Preserve existing admission authority field controllerUsername")

            def assignments(config):
                return {(item["team"], field, identity) for item in config.get("teamAssignments") or []
                        for field in ("users", "groups") for identity in item.get(field) or []}

            if assignments(old) != assignments(new):
                raise ValueError("Preserve existing admission authority field teamAssignments; changes need a separate approved procedure")
            if any(arg.startswith("--admission-pod-state-namespace") for arg in old_admission["args"]) and not admission["podsEnabled"]:
                raise ValueError("Preserve existing Pod admission protection with sandboxAdmission.podsEnabled=true")
            if old.get("validationMode") == "strict" and new.get("validationMode") != "strict":
                raise ValueError("Preserve existing strict admission protection with sandboxAdmission.validationMode=strict")
            for field in ("coordinationConfigMapName", "coordinationConfigMapUID", "placementConfigMapName",
                          "platformNamespace", "platformConfigMapName", "nativeExecution"):
                if old.get(field) and (type(old[field]) is not type(new.get(field)) or old[field] != new.get(field)):
                    raise ValueError(f"Preserve existing admission protection field {field}; changes need a separate approved procedure")

        native_fields = ("CETIRUN_SANDBOX_SOURCES", "CONNECTIONS_MCP_NATIVE_ENABLED", "CONNECTIONS_MCP_URL",
                         "WORKLOAD_TOKEN_AUDIENCE", "CONNECTIONS_MCP_CA_SECRET_NAME",
                         "CONNECTIONS_MCP_BROKER_NAMESPACE", "CONNECTIONS_MCP_BROKER_SERVICE_ACCOUNT")
        old_operator = self.current_control("cetirun-operator", "manager", native_fields)
        new_operator = self.rendered_control("cetirun-operator", "manager")
        old_sources = self.control_json(old_operator, "CETIRUN_SANDBOX_SOURCES")
        new_sources = self.control_json(new_operator, "CETIRUN_SANDBOX_SOURCES")
        for field in ("enabled", "nativeExecution", "plannedMigration", "bootstrapOwnedPrerequisites", "platformConfigMapName",
                      "homeClaimName", "skillsClaimName", "skillsCatalogConfigMapName", "placementConfigMapName",
                      "coordinationConfigMapName", "coordinationConfigMapUID", "admissionServiceAccount", "helpers"):
            if old_sources.get(field) and (type(old_sources[field]) is not type(new_sources.get(field)) or old_sources[field] != new_sources.get(field)):
                raise ValueError(f"Preserve existing native Sandbox protection field {field}; do not reset it through defaults")
        if old_sources.get("enabled") and old_admission is None:
            raise ValueError("Cannot inspect existing admission configuration for active native Sandbox sources")
        if old_operator and old_operator["env"]["CONNECTIONS_MCP_NATIVE_ENABLED"].lower() == "true":
            for field in native_fields[1:]:
                if old_operator["env"][field] != new_operator["env"].get(field, ""):
                    raise ValueError(f"Preserve existing native MCP protection field {field}")
        if old_operator:
            for field in ("CONNECTIONS_MCP_BROKER_NAMESPACE", "CONNECTIONS_MCP_BROKER_SERVICE_ACCOUNT"):
                if old_operator["env"][field] and old_operator["env"][field] != new_operator["env"].get(field, ""):
                    raise ValueError(f"Preserve existing Broker storage identity field {field}, including when native MCP is disabled")

        coordination = ("CONFIGURATION_COORDINATION_NAMESPACE", "CONFIGURATION_COORDINATION_NAME", "CONFIGURATION_COORDINATION_UID")
        old_portal = self.current_control("cetirun-portal", "portal", coordination)
        new_portal = self.rendered_control("cetirun-portal", "portal")
        if old_portal:
            for field in coordination:
                if old_portal["env"][field] and old_portal["env"][field] != new_portal["env"].get(field, ""):
                    raise ValueError(f"Preserve existing Portal coordination protection field {field}")
        old_mcp = self.current_control("cetirun-connections-mcp", "cetirun-connections-mcp", ("OPERATOR_NAMESPACE",))
        if old_mcp and old_mcp["env"]["OPERATOR_NAMESPACE"]:
            current_peer = self.kubectl("get", "deployment", "cetirun-connections-mcp", "--namespace", self.config["platform"]["namespace"],
                "-o", "jsonpath={.spec.template.metadata.labels.sandbox\\.cetirun\\.microsoft\\.com/peer-uid}")
            native = platform["cetirun-connections-mcp"]["nativeSandbox"]
            if not native["enabled"] or native["operatorNamespace"] != old_mcp["env"]["OPERATOR_NAMESPACE"] or not current_peer or native["peerUID"] != current_peer:
                raise ValueError("Preserve existing native MCP peer identity and operator namespace")

    def check_external_istio(self, document):
        kind, metadata = document["kind"], document["metadata"]
        namespace, name = metadata.get("namespace", self.config["istio"]["namespace"]), metadata["name"]

        def read(field):
            return self.kubectl("get", kind, name, "--namespace", namespace, "-o", "jsonpath={" + field + "}")

        def match(field, actual, expected):
            if actual != expected:
                raise ValueError(f"External Istio {kind} {namespace}/{name} {field} differs from the configured wiring")

        if kind in ("Gateway", "AuthorizationPolicy", "EnvoyFilter", "VirtualService", "ServiceEntry"):
            match("spec", json.loads(read(".spec") or "null"), document["spec"])
        elif kind == "Service" and name == "oauth2-proxy":
            current = json.loads(read(".spec") or "{}")
            match("selector", current.get("selector"), document["spec"]["selector"])
            match("type", current.get("type", "ClusterIP"), "ClusterIP")
            ports = [{**port, "protocol": port.get("protocol", "TCP")} for port in document["spec"]["ports"]]
            current_ports = [{**port, "protocol": port.get("protocol", "TCP")} for port in current.get("ports", [])]
            match("ports", current_ports, ports)
        elif kind == "Deployment" and name == "oauth2-proxy":
            template = document["spec"]["template"]
            match("selector", json.loads(read(".spec.selector") or "{}"), document["spec"]["selector"])
            match("serviceAccountName", read(".spec.template.spec.serviceAccountName"), template["spec"]["serviceAccountName"])
            labels = json.loads(read(".spec.template.metadata.labels") or "{}")
            match("workload labels", {key: labels.get(key) for key in template["metadata"]["labels"]}, template["metadata"]["labels"])
            container = next(item for item in template["spec"]["containers"] if item["name"] == "oauth2-proxy")
            prefix = '.spec.template.spec.containers[?(@.name=="oauth2-proxy")]'
            match("image", read(prefix + ".image"), container["image"])
            for field in ("command", "args", "ports", "envFrom"):
                match(field, json.loads(read(prefix + "." + field) or "[]"), container.get(field, []))
            match("environment names", sorted(read(prefix + ".env[*].name").split()), sorted(item["name"] for item in container["env"]))
            for entry in container["env"]:
                field = prefix + '.env[?(@.name=="' + entry["name"] + '")]'
                if "valueFrom" in entry:
                    match("environment reference " + entry["name"], json.loads(read(field + ".valueFrom") or "null"), entry["valueFrom"])
                else:
                    match("environment " + entry["name"], read(field + ".value"), entry["value"])
        elif kind == "ServiceAccount" and name == "oauth2-proxy":
            match("Workload Identity client", read(r".metadata.annotations.azure\.workload\.identity/client-id"),
                  metadata.get("annotations", {}).get("azure.workload.identity/client-id", ""))

    def preflight(self):
        c = self.config
        target = self.kubectl("config", "view", "--minify", "-o", 'jsonpath={.contexts[0].context.cluster}{"\\n"}{.clusters[0].cluster.server}')
        if target.splitlines() != [c["target"]["cluster"], c["target"]["server"]]:
            raise ValueError("The selected context does not match target.cluster and target.server")
        if (self.resource("namespace", "kube-system") or {}).get("uid") != c["target"]["clusterUID"]:
            raise ValueError("target.clusterUID does not match this cluster's kube-system UID")
        version = json.loads(self.kubectl("version", "-o", "json"))["serverVersion"]
        if (int(version["major"]), int(re.match(r"\d+", version["minor"])[0])) < (1, 37):
            raise ValueError("Enforced admission requires Kubernetes 1.37 or newer")
        for kind in ("gateways.networking.istio.io", "virtualservices.networking.istio.io", "authorizationpolicies.security.istio.io"):
            if not self.resource("customresourcedefinition", kind):
                raise ValueError(f"Managed Istio prerequisite is missing: {kind}")
        for field in ("controlPlane", "ingress"):
            namespace, deployment = c["istio"][f"{field}Namespace"], c["istio"][f"{field}Deployment"]
            # A missing Deployment would only fail rollout status, whose output is
            # withheld. AKS managed Istio adds the revision to these names.
            if not self.resource("deployment", deployment, namespace):
                revisioned = f"{deployment}-{c['istio']['revision']}"
                hint = f"; AKS managed Istio adds the revision to the name, and {revisioned} exists" \
                    if self.resource("deployment", revisioned, namespace) else ""
                raise ValueError(f"The configured istio.{field}Deployment {namespace}/{deployment} does not exist{hint}")
            self.kubectl("rollout", "status", f"deployment/{deployment}", "--namespace", namespace, "--timeout=180s")
        if not self.resource("serviceaccount", c["istio"]["ingressServiceAccount"], c["istio"]["ingressNamespace"]):
            raise ValueError("The configured Istio ingress ServiceAccount is missing")
        for name in CHARTS:
            section = c[name]
            metadata = self.resource("namespace", section["namespace"])
            self.namespaces[section["namespace"]] = metadata
            label = (metadata or {}).get("labels", {}).get("istio.io/rev")
            if name != "istio" and label and label != c["istio"]["revision"]:
                raise ValueError(f"{section['namespace']} uses another Istio revision; refusing to replace it")
            releases = json.loads(self.helm("list", "--deployed", "--failed", "--pending", "--uninstalling", "--uninstalled", "--superseded",
                                            "--namespace", section["namespace"], "--filter", f"^{section['release']}$", "-o", "json"))
            if releases:
                if releases[0]["status"].startswith("pending-"):
                    raise ValueError(f"Release {section['release']} has an unfinished operation")
                self.existing_releases.add(name)
            for doc in self.documents[name]:
                kind, metadata = doc["kind"], doc["metadata"]
                namespace = "" if kind in CLUSTER_KINDS else metadata.get("namespace", section["namespace"])
                current = self.resource(kind, metadata["name"], namespace)
                if current:
                    annotations = current.get("annotations", {})
                    if (annotations.get("meta.helm.sh/release-name"), annotations.get("meta.helm.sh/release-namespace")) != (section["release"], section["namespace"]):
                        raise ValueError(f"Refusing to adopt {kind} {namespace}/{metadata['name']} from another owner")
                    if kind == "Secret" and not set(doc.get("data", {})).issubset(self.secret_keys(namespace, metadata["name"])):
                        raise ValueError(f"Retained Secret {namespace}/{metadata['name']} is missing required keys; refusing regeneration")
                    if name == "istio" and c["istio"].get("mode", "manage") == "external" and kind != "Secret":
                        self.check_external_istio(doc)
                elif name == "istio" and c["istio"].get("mode", "manage") == "external" and kind != "Secret":
                    raise ValueError(f"External Istio is missing required {kind} {namespace}/{metadata['name']}")
                elif name in self.existing_releases and kind == "Secret":
                    raise ValueError(f"Retained Secret {namespace}/{metadata['name']} is missing; refusing regeneration")
        self.check_existing_controls()
        self.check_agent_mcp_conflicts()
        for kind, namespace, name, key in self.references():
            metadata = self.resource(kind, name, namespace)
            if not metadata:
                raise ValueError(f"Required {kind} reference is missing: {namespace}/{name}")
            admission = self.values["platform"]["cetirun-operator"]["sandboxAdmission"]
            if kind == "ConfigMap" and name == admission["coordinationConfigMapName"] and metadata.get("uid") != admission["coordinationConfigMapUID"]:
                raise ValueError("The admission coordination ConfigMap UID does not match the configured authority")
            if kind == "Secret" and key and key not in self.secret_keys(namespace, name):
                raise ValueError(f"Required Secret key is missing: {namespace}/{name}:{key}")
        mesh = self.kubectl("get", "configmap", c["istio"]["meshConfigMap"], "--namespace", c["istio"]["controlPlaneNamespace"], "--ignore-not-found", "-o", "json")
        if mesh.strip():
            document = json.loads(mesh)
            self.mesh_uid = document["metadata"]["uid"]
            _, changed = MESH.configmap_patch(document, self.oauth_service())
            if changed and c["istio"].get("mode", "manage") == "external":
                raise ValueError("External Istio has no matching OAuth2 provider/header configuration")
        else:
            # The helper creates a missing ConfigMap. AKS managed Istio reads only
            # istio-shared-configmap-<revision>, so another name leaves the OAuth2
            # provider out of the mesh.
            shared = f"istio-shared-configmap-{c['istio']['revision']}"
            if shared != c["istio"]["meshConfigMap"] and self.resource("configmap", shared, c["istio"]["controlPlaneNamespace"]):
                raise ValueError(f"The configured istio.meshConfigMap {c['istio']['controlPlaneNamespace']}/{c['istio']['meshConfigMap']} "
                                 f"does not exist, and AKS managed Istio reads {shared}, which exists")
            if c["istio"].get("mode", "manage") == "external":
                raise ValueError("External Istio requires the existing OAuth2 mesh provider")
        if c["istio"].get("mode", "manage") == "external" and "istio" not in self.existing_releases:
            raise ValueError("istio.mode=external requires the named existing CetiRun Istio release")
        tls = self.resource("Secret", "cetirun-tls", c["istio"]["ingressNamespace"])
        owner = (tls or {}).get("annotations", {})
        owns_tls = (owner.get("meta.helm.sh/release-name"), owner.get("meta.helm.sh/release-namespace")) == (c["istio"]["release"], c["istio"]["namespace"])
        if c["istio"].get("tlsMode", "external") == "retain":
            if not owns_tls or "istio" not in self.existing_releases:
                raise ValueError("tlsMode=retain requires the existing release's chart-owned cetirun-tls Secret")
        elif owns_tls and c["istio"].get("mode", "manage") == "manage":
            raise ValueError("The Istio release owns cetirun-tls; use tlsMode=retain or a separately approved ownership migration")
        database = "cetirun-ai-gateway-postgresql"
        if self.resource("PersistentVolumeClaim", f"data-{database}-0", c["platform"]["namespace"]) and not self.resource("Secret", database, c["platform"]["namespace"]):
            raise ValueError("Retained PostgreSQL data has no password Secret; refusing to generate a replacement")
        self.check_tls()
        for entry in RELEASE.inventory()["images"]:
            self.run(["docker", "buildx", "imagetools", "inspect", f"{c['release']['registry']}/{entry['name']}:{c['release']['version']}"])
        for image in self.values["platform"]["cetirun-operator"]["customRuntime"]["approvedImages"]:
            self.run(["docker", "buildx", "imagetools", "inspect", image])
        for entry in RELEASE.inventory()["charts"]:
            self.run(["helm", "show", "chart", f"oci://{c['release']['registry']}/helm/{entry['name']}", "--version", c["release"]["version"]])

    def oauth_service(self):
        return f"oauth2-proxy.{self.config['istio']['namespace']}.svc.cluster.local"

    def upgrade(self, name, values=None):
        section = self.config[name]
        print(f"==> Upgrade {section['namespace']}/{section['release']}", flush=True)
        args = ["upgrade", "--install", section["release"], str(self.charts[name]), "--namespace", section["namespace"],
                "--values", str(self.files[values or name]), "--timeout", "10m"]
        if name == "istio" and section.get("tlsMode") == "retain":
            args.append("--reuse-values")
        self.helm(*args)

    def ready(self, name):
        section = self.config[name]
        workloads = [doc for doc in self.documents[name] if doc["kind"] in ("Deployment", "StatefulSet")]
        workloads.sort(key=lambda doc: (doc["kind"] != "StatefulSet", "valkey" not in doc["metadata"]["name"]))
        for doc in workloads:
            namespace, workload = doc["metadata"].get("namespace", section["namespace"]), doc["metadata"]["name"]
            self.kubectl("rollout", "status", f"{doc['kind'].lower()}/{workload}", "--namespace", namespace, "--timeout=600s")
            if workload in ("cetirun-operator", "cetirun-portal", "cetirun-agent-broker", "cetirun-connections-mcp"):
                selector = ",".join(f"{key}={value}" for key, value in doc["spec"]["selector"]["matchLabels"].items())
                output = self.kubectl("get", "pods", "--namespace", namespace, "--selector", selector, "-o",
                                      'jsonpath={range .items[*]}{.spec.containers[*].name}{" "}{.spec.initContainers[*].name}{"\\n"}{end}')
                if not output.strip() or any("istio-proxy" not in line.split() for line in output.splitlines()):
                    raise ValueError(f"{workload} is missing the required Istio sidecar")
                if workload == "cetirun-agent-broker" and any("auth-sdk" not in line.split() for line in output.splitlines()):
                    raise ValueError("Broker pods are missing the Auth SDK")

    def execute(self):
        self.preflight()
        c = self.config
        for name in CHARTS:
            namespace = c[name]["namespace"]
            if self.namespaces[namespace] is None:
                self.kubectl("create", "namespace", namespace)
            if name != "istio" and (self.namespaces[namespace] or {}).get("labels", {}).get("istio.io/rev") != c["istio"]["revision"]:
                self.kubectl("label", "namespace", namespace, f"istio.io/rev={c['istio']['revision']}", "--overwrite=false")
        if c["istio"].get("mode", "manage") == "manage":
            self.upgrade("istio")
        self.ready("istio")
        command = [sys.executable, "-B", str(ROOT / "scripts/ensure-oauth2-proxy-id-token-header.py"),
                   "--context", c["target"]["context"], "--namespace", c["istio"]["controlPlaneNamespace"],
                   "--name", c["istio"]["meshConfigMap"], "--service", self.oauth_service()]
        if self.mesh_uid:
            command += ["--uid", self.mesh_uid]
        if c["istio"].get("mode", "manage") == "manage":
            self.run(command)
        self.upgrade("broker")
        self.ready("broker")
        # Never withdraw existing admission protection during an upgrade.
        if not self.enforced:
            self.upgrade("platform", "bootstrap")
            self.ready("platform")
        else:
            self.kubectl("rollout", "status", "deployment/cetirun-sandbox-admission", "--namespace", c["platform"]["namespace"], "--timeout=180s")
        self.upgrade("platform")
        self.ready("platform")
        probe = {"apiVersion": "cetirun.microsoft.com/v1alpha1", "kind": "Team",
                 "metadata": {"name": "cetirun-admission-deployment-check"}, "spec": {}}
        self.kubectl("create", "--dry-run=server", "-f", "-", input_text=json.dumps(probe))
        print("Platform releases are ready and the admission dry-run succeeded. This helper did not create or replace Agents, HOME or user keys.")
        print("Not qualified by this check: model calls, OAuth login, native Sandbox execution/physical fencing, or CetiAdmin token coexistence.")

    def plan(self):
        references = {(kind, namespace, name) for kind, namespace, name, _ in self.references()}
        return {"target": {key: self.config["target"][key] for key in ("context", "cluster", "server", "clusterUID")},
                "release": {key: self.config["release"][key] for key in ("registry", "version", "gitSHA")},
                "stages": ["verify target, managed Istio, references, ownership and all artifacts",
                           "CetiRun Istio/OAuth2 release and mesh provider", "isolated Broker/Auth SDK release",
                           "platform data services, Operator, Portal Chat, MCP, Custom/terminal and Memory bindings",
                           "admission server/TLS readiness before fail-closed webhooks", "all workloads ready and admission server dry-run"],
                "releases": {name: {"namespace": self.config[name]["namespace"], "release": self.config[name]["release"],
                                     "workloads": [doc["metadata"]["name"] for doc in docs if doc["kind"] in ("Deployment", "StatefulSet")]}
                             for name, docs in self.documents.items()},
                "artifacts": {kind: [entry["name"] for entry in entries] for kind, entries in RELEASE.inventory().items() if kind in ("images", "charts")},
                "referenceCounts": dict(Counter(kind for kind, _, _ in references)),
                "preserves": ["existing Agents", "HOME/PVCs", "OAuth cookie and database keys", "other Helm releases"],
                "notQualified": ["native Sandbox execution and physical fencing", "CetiAdmin token coexistence", "live model/OAuth calls"]}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True, type=Path, help="explicit environment YAML; never sourced as shell code")
    parser.add_argument("--plan", action="store_true", help="validate inputs and render locally without cluster/registry calls")
    args = parser.parse_args(argv)
    try:
        config, values = configuration(args.config)
        with tempfile.TemporaryDirectory(prefix="cetirun-platform-") as temporary:
            deployment = Deployment(config, values, Path(temporary))
            deployment.render()
            if args.plan:
                print(json.dumps(deployment.plan(), indent=2))
            else:
                deployment.execute()
    except yaml.YAMLError as error:
        mark = getattr(error, "problem_mark", None)
        location = f" at line {mark.line + 1}, column {mark.column + 1}" if mark else ""
        print(f"ERROR: Invalid YAML{location}; input contents withheld", file=sys.stderr)
        return 1
    except (ValueError, KeyError, TypeError, OSError, RuntimeError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
