# CetiRun Platform chart

The normal deployment entry point is `make deploy DEPLOY_CONFIG=/path/to/environment.yaml`.
It manages the CetiRun Istio/OAuth2 Proxy release, the Broker and Auth SDK in a
private credential namespace, and the platform release. The platform includes
the Operator, admission server, Portal, AI Gateway, PostgreSQL, Valkey, and
Connections MCP. The same configuration enables Portal Chat and supplies
Custom runtime, terminal, Memory, and shared Skills/HOME references.

The `cetirun-platform` Helm chart is the lower-level umbrella for the last
group. Its component switches remain available for chart consumers, but the
normal deployment entry point requires the full set. There is no `--full` mode.
It does not create cloud identities, issue certificates, enroll a Sandbox
platform, or create Agent instances.

## Deploy the platform

Prepare an environment file outside the repository. The
[synthetic example](ci/deployment-example.yaml) shows the input shape and is
used by offline tests. Its certificate framing, image digests, identities,
prices, and owner are test data, not deployable credentials or qualification.

The file contains these inputs:

| Input | Meaning |
| --- | --- |
| `target` | Explicit kubeconfig context, cluster name, HTTPS API server, and independently obtained `kube-system` namespace UID |
| `release` | Registry including any repository prefix, release version, and full source Git SHA |
| `domain`, `identityRealm`, `ownerAlias` | Public hostname, Portal Chat identity realm, and the real platform owner's Portal administrator alias |
| `identity` | Entra tenant, Portal, blueprint, authorization, and encryption client IDs, and the versioned Key Vault key URL |
| `istio` | Separate release/namespace, existing managed control plane and ingress identities, mesh ConfigMap, and Istio Chart values |
| `broker` | Separate credential namespace/release and Broker Chart values, including existing TLS and optional provider credential Secret references |
| `platform` | Platform release/namespace and umbrella values, including admission, provider credentials, model deployments, and qualified Custom images |
| `sharedSkills` | The runtime chart's existing shared Skills storage fields, including storage class, resource group, account, and container |

The existing managed Istio control plane, ingress workload, DNS, cloud identity
federation/permissions, storage classes, and referenced Secrets are prerequisites.
Provision them through the environment's approved workflow. The helper does not
call `az`, retrieve Key Vault secrets, change subscriptions, or change
`current-context`. Every cluster operation carries `target.context`; the helper
checks its cluster name, API origin, and cluster UID before a write.

Use the existing Helm values under each release's `values` field. The helper
connects component URLs, namespaces, image coordinates, and identity fields from
the common inputs. Keep deployment-specific overrides in this file. Custom
images require digest-pinned `approvedImages`, an explicit model-access mode,
and the storage settings whose UID/GID behavior you qualified. An image being
published does not establish that qualification.

Configure the logical model `auto` with `auto_router/complexity_router`,
`classifier_type: jev`, and `jev_classifier_config.deployment_name: decision-1`.
The separate `decision-1` entry names the real `azure_ai/<deployment>` and uses
`model_info.base_model: Microsoft-Decision-1` or
`azure_ai/Microsoft-Decision-1` with `mode: evaluation`. Its `api_version` is
optional; omission preserves the provider's default, and an explicit value is
passed through. The endpoint and Secret environment reference remain required. All four
tiers and the real provider deployments belong in the environment file. The
router's price fields are required guard metadata, not an average of route
prices. Use the highest real tier rates; billing remains on the selected
backend and classifier. Do not copy the synthetic fixture prices into service.

Run the offline plan first:

```bash
make deploy-plan DEPLOY_CONFIG=/path/to/environment.yaml
```

This command checks required inputs and renders real local charts in a temporary
directory. It prints releases, workloads, all 13 image/8 chart artifacts, and
external reference names. It does not contact a cluster or registry and cannot
verify live TLS, credentials, storage, identity federation, or model availability.
Python with PyYAML and Helm must already be installed.

After the target deployment is authorized, run:

```bash
make deploy DEPLOY_CONFIG=/path/to/environment.yaml
```

This command also requires kubectl, OpenSSL, and Docker buildx for read-only artifact
inspection, with their normal authentication configured beforehand. It checks
release ownership, required Secret key names, storage references, and
image/chart availability before its first persistent write. It refuses to adopt
unmanaged objects or resources from another Helm release.

Before a write, it also verifies the configured admission CA and service DNS
name, the MCP/Broker server chains, and the Broker client's CA, client purpose,
and existing exact MCP URI identity contract. The ingress certificate must be
currently valid and cover `domain`; this is not a public-browser trust test.
Only the referenced `tls.crt` and CA fields are extracted into temporary public
certificate files. Kubernetes still returns the whole Secret for a JSONPath
request and requires `get Secret` permission. The helper does not parse, save,
or log private-key, cookie, or provider-key values. A failed OpenSSL check stops
before any persistent cluster write. It does not issue or rotate certificates.

The helper installs or upgrades CetiRun Istio, waits for OAuth2 Proxy, and
reconciles only the named `oauth2-proxy` mesh provider through the existing
resource-version-checked helper. A provider for another service or a replaced
ConfigMap stops deployment. Set `istio.mode: external` only when the named
release and provider are already managed elsewhere; that mode verifies them
without upgrading the release or patching the mesh.

The Broker and its Auth SDK start next in the credential namespace. The helper
then installs the platform and waits for every Deployment and StatefulSet,
including PostgreSQL, Valkey, MCP, and the admission server. On a new
installation, the admission server starts before fail-closed webhooks are
installed. An upgrade never turns off existing admission enforcement. A final
server-side dry-run of a Team creation checks the served admission path without
persisting a Team. Any failed read, render, upgrade, rollout, or dry-run returns
a nonzero exit status. There is no automatic rollback or ownership transfer.

Before an upgrade, the helper extracts selected existing deployment control
fields and the standard admission webhooks. It refuses to remove Pod admission,
replace strict validation with compatible validation, change an existing
coordination authority, or reset active native execution settings through
defaults. The environment file must preserve those controls. An existing
admission server must expose its control configuration. Unreadable observations
or externally referenced control values stop the upgrade. Changes to these protections need a separate approved
procedure; the helper does not migrate arbitrary stored Helm values.

The Operator's `connectionsMCP.runtimeTypes`, the Portal's
`agentTools.runtimeTypes`, and the Broker's `toolService.runtimeTypes` must name
the same runtimes. A consistent explicit `[hermes]` selection remains supported.
For existing Agents in a selected runtime, the helper extracts the name,
runtime, Team/Sandbox markers, and necessary ServiceAccount settings. It does
not output owner details, unrelated Helm values, or conversation data. It
rejects ordinary Agents that depend on a custom ServiceAccount, an API role,
or an API token before enabling MCP can override those settings. Policy-bound
Sandbox Agents use the separate native path and are not treated as ordinary
Agents by this check. Kubernetes still returns complete objects for these
projections; the helper only parses the selected fields.
The caller needs read access to these Agent, Team, Deployment, and webhook
resources. The helper does not grant itself additional permissions.

### Preserve certificates and retained data

`istio.tlsMode: external` uses the existing `cetirun-tls` Secret in the ingress
namespace. If the Istio release owns that Secret, this mode fails before writes
because disabling `tls.enabled` would delete the release's certificate.

For an existing chart-owned certificate, set `istio.tlsMode: retain`. The helper
requires the existing release and matching Secret ownership, omits `tls` from
the actual override file, and uses `--reuse-values` for that Istio upgrade.
Helm retains the prior TLS values without placing private-key material in the
helper's override file. Switching ownership modes requires a separate migration.

The offline render uses synthetic cookie/TLS values only in a separate temporary
render file. The helper never applies that manifest. Actual upgrades use the
normal charts and their server-side Secret lookup. Missing retained cookie or
database Secrets fail before writes; they are not replaced with new keys.
The helper never deletes Agents or HOME/PVCs, creates canary workloads, rotates
user keys, or adopts resources owned by another release. Repeating the command
reconciles the same releases and preserves existing admission enforcement.
Verify actual cookie, certificate, and database continuity in a separately
authorized environment test.

### Deployment readiness is not runtime qualification

Broker/MCP readiness endpoints do not establish provider or Auth SDK API
availability. The helper checks workload rollouts, required containers and
Istio sidecars, reference wiring, and the admission dry-run. It does not run
an OAuth sign-in, a provider token exchange, a real model call, or a terminal
session. The Gateway's own health probes follow its configured model settings
and can make model calls after startup.

Native Sandbox execution and physical writer fencing remain separate
qualification work. The default does not enable `sandboxSources` or manufacture
capability registrations. Existing native configuration must retain its real
qualification inputs. The deployment does not add CetiAdmin assets or resolve
CetiAdmin ServiceAccount/token coexistence with MCP. An existing ordinary
Agent with that conflict stops deployment before writes and names the remaining
dependency. The helper does not silently remove OpenClaw from the allowlist.
Requests to create
instances through this file fail explicitly; existing instances are preserved.

## Use the component chart directly

CetiRun Connections MCP can optionally be enabled by direct chart consumers. Install
the Agent Broker separately in its private credential namespace; the umbrella
rejects colocating an enabled Broker with the Tool service. Provision TLS and
OAuth separately, then configure the MCP Broker HTTPS origin, Portal/Operator
management HTTP origins, and Operator MCP URL/CA-only Secret. The sample
`app/cetirun-connections-mcp/chart/ci/render-values.yaml` contains synthetic render-only
configuration; it does not create certificates or authorize deployment.

The bundled Operator chart manages the four CetiRun CRDs as Helm templates,
including schema upgrades. Use values which explicitly confirm the
separately installed Istio prerequisites:

```bash
ISTIO_REVISION="$(kubectl get pods -n aks-istio-system -l app=istiod \
  -o jsonpath='{.items[0].metadata.labels.istio\.io/rev}')"
test -n "${ISTIO_REVISION}"
kubectl create namespace cetirun-system --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace cetirun-system "istio.io/rev=${ISTIO_REVISION}" --overwrite

helm dependency build app/cetirun-platform
helm upgrade --install cetirun-platform app/cetirun-platform \
  --namespace cetirun-system \
  --set istio.enabled=true \
  --set-string cetirun-operator.domainSuffix=cetirun.example.com \
  --set-string "cetirun-operator.istioRevision=${ISTIO_REVISION}" \
  --set-string cetirun-operator.istioGateway=aks-istio-ingress/cetirun-gateway \
  --set-string cetirun-operator.cetirun.helmValues.networkPolicy.istioIngressNamespace=aks-istio-ingress \
  --set cetirun-portal.istio.host=cetirun.example.com \
  --set cetirun-portal.istio.gateway=aks-istio-ingress/cetirun-gateway \
  --set cetirun-portal.istio.gatewayNamespace=aks-istio-ingress \
  --set cetirun-portal.istio.gatewayServiceAccount=aks-istio-ingressgateway-external

kubectl rollout status deployment/cetirun-portal -n cetirun-system --timeout=180s
kubectl get pods -n cetirun-system -l app.kubernetes.io/name=cetirun-portal \
  -o jsonpath='{range .items[*]}{.spec.containers[*].name} {.spec.initContainers[*].name}{"\n"}{end}' | grep -qw istio-proxy
```

The final command verifies that the Portal has the Istio sidecar required for
its authorization policies; it lists init containers too, because Istio can
inject the sidecar as a native sidecar, an init container that keeps running,
as the AKS Istio add-on does. Set `cetirun-operator.istioGateway` to the same
`<namespace>/<name>` as `cetirun-portal.istio.gateway` when using a custom
gateway. `cetirun-portal.istio.gatewayNamespace` and
`gatewayServiceAccount` identify the selected gateway workload, which can run
in a namespace other than its Gateway resource. Also set
`cetirun-operator.cetirun.helmValues.networkPolicy.istioIngressNamespace` to
that gateway workload's namespace; otherwise the instance NetworkPolicies block
ingress traffic.

Helm upgrades the CRD templates with this release; no separate CRD apply script
is required for compatible changes. The `helm.sh/resource-policy: keep`
annotation retains CRDs and their objects on uninstall. It does not protect
against an incompatible schema change or downgrade. The Operator chart defaults
`sandboxAdmission.validationMode` to `compatible`, retaining known validation and
authorization checks while tolerating newer CR fields. See the
[schema and binary upgrade contract](../cetirun-operator/README.md#schema-and-binary-upgrades).
Previously unmanaged CRDs and cross-release ownership transfers still require
a separately reviewed handover; this chart does not forcibly adopt them.

The deployment helper accepts `ISTIO_GATEWAY_NAME` and
`ISTIO_GATEWAY_NAMESPACE` for the Gateway resource, and
`ISTIO_INGRESS_NAMESPACE` (defaulting to the Gateway namespace) plus
`ISTIO_GATEWAY_SERVICE_ACCOUNT` for the Gateway workload identity.

To install in a namespace other than `cetirun-system`, set both
`cetirun-operator.namespace` and `cetirun-portal.aiGateway.catalogNamespace` to
it: the operator publishes the shared models in its own namespace, where the
Portal reads them. The deployment helper sets both from `NAMESPACE`, and the
chart refuses a Portal that would look for them elsewhere.

When using the umbrella, configure the separate Istio release to reach the
co-located Portal before installing it:

```bash
PORTAL_AUTH_NAMESPACE=cetirun-system ./infra/istio/deploy.sh
```

Each component chart remains independently installable. Moving existing
standalone releases into this umbrella requires a separately planned handover;
the chart never adopts resources owned by another release.

## Sandbox creation profiles

Configure `cetirun-portal.sandboxRuntimeProfiles` for each supported built-in
Sandbox runtime. Each entry contains `chartVersion`, a digest-pinned `image`,
and `port` (18789 for OpenClaw, 8642 for Hermes). The chart version must match
the Operator's configured runtime chart, and that chart's profile must approve
the image and port. Only configure harnesses supported by the selected platform;
the common native executor supports OpenClaw and Hermes, while retained
single-start records are OpenClaw-only.

The Portal records these inputs in the initial Agent creation request, so users
do not enter deployment coordinates in the creation form. Explicit image/port
requests remain subject to admission. By default, missing or invalid deployment
profiles reject creation before any Agent, namespace or Secret is written.

With the Operator's `sandboxSources.nativeExecution` enabled, the deployment may set
`cetirun-portal.sandboxRuntimeDefaultsFromController: true`. This permits missing
profiles to use trusted chart/image/port defaulting in the common executor.
Configured profiles are still validated. The platform chart rejects this Portal
opt-in unless the Operator and its native sources are enabled.
For standalone Portal releases, verify the matching Operator configuration
before enabling it, and turn it off before disabling native execution.
These defaults do not authorize a runtime or change the
selected AgentPolicy/Sandbox checks.

When `cetirun-operator.sandboxAdmission` is enabled, the chart adds the Portal's
service account, `system:serviceaccount:<release namespace>:cetirun-portal`, to
the admission administrators. The Portal writes Agents, Teams, Sandboxes and
AgentPolicies with it, and the enforced admission webhook refuses its writes to
policy-bound Agents otherwise, for example saving an owner's GitHub Copilot
models after a sign-in. `cetirun-operator.sandboxAdmission.portalServiceAccount`
names the account, `cetirun-portal` by default. An installation that already
lists it in `adminUsers` keeps the same admission configuration. It does not
count as the administrator that `adminUsers` or `adminGroups` must name:
reauthorization, for example, has no Portal page.

- With a custom `cetirun-portal.serviceAccount.name`, set `portalServiceAccount`
  to the same name; the chart refuses different names.
- With the bundled Portal disabled, clear `portalServiceAccount`; the chart
  refuses it otherwise. List a separately installed Portal's service account in
  `adminUsers`.
- An upgrade with `--reuse-values` from an older release does not set
  `portalServiceAccount`. Set it, or list the Portal's service account in
  `adminUsers`; the chart refuses enforced admission with neither.

Administrator groups cannot be blanket groups such as `system:authenticated` or
`system:serviceaccounts:<namespace>`: the admission server refuses to start with
them, so the chart refuses them too.

## AI Gateway

OpenClaw and Hermes Agents take their models from the AI Gateway, so the
platform requires it: the chart refuses to install with
`cetirun-operator.aiGateway.enabled` or `cetirun-portal.features.aiGatewayEnabled`
turned off. Hermes Agents pinned to a chart version without Hermes' Gateway
provider keep their own model provider and sign-in until their chart is
upgraded. By default the release
also installs the Gateway with its PostgreSQL and Valkey, named
`cetirun-ai-gateway`, in the release namespace, which must be the operator's
namespace: the operator reads the Gateway master key from there. See the
[Gateway chart](../cetirun-ai-gateway/chart/README.md) for its settings, under
`cetirun-ai-gateway` here.

Shared models are the models set in `cetirun-ai-gateway.models`. The Gateway
chart gives each one a deployment ID and an access group derived from its name,
and the operator offers it through them: Agents without a policy are offered
every one, and policy-bound Agents the ones their AgentPolicy grants. While the
Gateway blocks unpriced models, as it does by default, each one also needs a
price. For example, with the provider key in a Secret you create in the release
namespace:

```yaml
cetirun-ai-gateway:
  providerCredentials:
    existingSecret: gateway-provider-keys  # holds AZURE_OPENAI_KEY
  models:
    - model_name: gpt-4o
      litellm_params:
        model: azure/my-gpt-4o-deployment
        api_base: https://example.openai.azure.com/
        api_key: os.environ/AZURE_OPENAI_KEY
        # Must support the Responses API: agents' tool calls are sent there.
        api_version: "2025-04-01-preview"
      model_info:
        # The price; or set input_cost_per_token and output_cost_per_token.
        base_model: azure/gpt-4o
```

These models no longer need the access group `cetirun-shared`; a model that
has it keeps working. Editing a model's settings, such as its endpoint,
provider parameters or price, keeps its access group, so Agents keep using it
and are not restarted for it. An upgrade of the Gateway does not interrupt
them either: its pods keep the configuration they started with until they are
replaced, and the operator applies a changed model list once the rollout is
over.

Shared models are optional. Without them OpenClaw Agents use their owners'
GitHub Copilot; with `cetirun-operator.aiGateway.copilotProxy.enabled` turned
off, the Portal creates no OpenClaw Agent until a shared model is offered. The
Gateway's health checks do not probe the Agents' GitHub Copilot routes, since
each probe is a real call on the owner's Copilot allowance;
`cetirun-operator.aiGateway.copilotProxy.healthChecks.enabled` turns their
probes on. A model added in the console is shared only with the access group
`cetirun-shared` and a price, and only with Agents without a policy. Open the
console with:

```bash
kubectl get secret cetirun-ai-gateway-auth -n cetirun-system \
  -o jsonpath='{.data.ui-password}' | base64 -d; echo
kubectl port-forward -n cetirun-system svc/cetirun-ai-gateway 4000:4000
```

Then open <http://localhost:4000/ui> and sign in as `admin`.

A Gateway installed separately can be used instead: set
`cetirun-ai-gateway.enabled=false` and install it in the operator namespace
under the name `cetirun-ai-gateway`, with `operatorIntegration: true`.
Elsewhere, set `cetirun-operator.aiGateway.url` to it and copy its master key
into the operator namespace as the `cetirun-ai-gateway-auth` Secret.

New Agents need a Team. Create one before users create Agents, in the Portal's
administration pages or with a manifest:

```bash
kubectl apply -f - <<'EOF'
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Team
metadata:
  name: example-team
spec: {}
EOF
```
