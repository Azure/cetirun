# CetiRun Platform chart

Deploy with `make deploy DEPLOY_CONFIG=/path/to/environment.yaml`.
This command manages the CetiRun Istio/OAuth2 Proxy release, the Broker and
Auth SDK in a private credential namespace, and the platform release. The
platform includes the Operator, admission server, Portal, AI Gateway,
PostgreSQL, Valkey, and Connections MCP. The same configuration enables Portal
Chat and supplies Custom runtime, terminal, Memory, and shared Skills/HOME
references. The optional [Skill Marketplace](#skill-marketplace) is installed
only when the environment file configures it.

The `cetirun-platform` Helm chart contains the platform components. Its
switches stay available to chart consumers. The full set is necessary for the
deployment script without a `--full` option. Cloud identities, certificates,
Sandbox enrollment, and Agent creation are not part of this entry point.

## Deploy the platform

Prepare an environment file outside the repository. The
[synthetic example](ci/deployment-example.yaml) shows the input shape. Offline
tests use this example. Its certificate framing, image digests, identities,
prices, and owner are test data, not deployable credentials or qualification.

The file contains these inputs:

| Input | Meaning |
| --- | --- |
| `target` | Explicit kubeconfig context, cluster name, HTTPS API server, and the `kube-system` namespace UID that you get independently |
| `release` | Registry with any repository prefix, release version, and full source Git SHA |
| `domain`, `identityRealm`, `ownerAlias` | Public hostname, Portal Chat identity realm, and the Portal administrator alias of the real platform owner |
| `identity` | Entra tenant, Portal, blueprint, authorization, and encryption client IDs, and the versioned Key Vault key URL |
| `istio` | Separate release/namespace, the managed control plane and ingress identities that exist, mesh ConfigMap, and Istio Chart values |
| `broker` | Separate credential namespace/release and Broker Chart values, for example, references to TLS Secrets that exist and optional references to provider credential Secrets |
| `platform` | Platform release/namespace and umbrella values, for example, admission, provider credentials, model deployments, and qualified Custom images |
| `sharedSkills` | The shared Skills storage fields that exist in the runtime chart, for example, storage class, resource group, account, and container |
| `skillMarketplace` | Optional. The Git repository of the [Skill Marketplace](#skill-marketplace), its credentials Secret, and its Blob NFS container |

The managed Istio control plane, ingress workload, DNS, cloud identity
federation/permissions, storage classes, and referenced Secrets are
prerequisites that must already exist. Provision them through the environment's
approved workflow. The helper does not call `az`, get Key Vault secrets, change
subscriptions, or change `current-context`. Every cluster operation uses
`target.context`. Before a write, the helper examines the cluster name, API
origin, and cluster UID of that context.

AKS managed Istio adds the revision to the names of the control plane
Deployment, the ingress gateway Deployment, and the shared mesh ConfigMap. An
example is `aks-istio-ingressgateway-external-asm-1-30`. The ingress
ServiceAccount has no revision in its name. Read the names from the cluster.
Before a write, the helper stops if a configured Deployment does not exist. It
also stops if `istio.meshConfigMap` does not exist but
`istio-shared-configmap-<revision>` exists.

Use the available Helm values under the `values` field of each release. The
helper connects component URLs, namespaces, image coordinates, and identity
fields from the common inputs. Custom images must have digest-pinned
`approvedImages`, an explicit model-access mode, and the storage settings whose
UID/GID behavior you qualified. The publication of an image does not establish
that qualification.

Configure the logical model `auto` with `auto_router/complexity_router`,
`classifier_type: jev`, and `jev_classifier_config.deployment_name: decision-1`.
The separate `decision-1` entry refers to the real `azure_ai/<deployment>` and
uses `model_info.base_model: Microsoft-Decision-1` or
`azure_ai/Microsoft-Decision-1` with `mode: evaluation`. Its `api_version` is
optional. Omission keeps the provider's default, and an explicit value is
passed through. The endpoint and Secret environment reference are still
necessary.

Put all four tiers and the real provider deployments in the environment file.
The router's price fields are necessary guard metadata, not an average of route
prices. Use the highest real tier rates. Billing stays on the selected backend
and classifier. Do not copy the synthetic fixture prices into service.

Run the offline plan first:

```bash
make deploy-plan DEPLOY_CONFIG=/path/to/environment.yaml
```

The plan validates the inputs and renders local charts in a temporary
directory. Its JSON lists the target, releases, workloads, all 14 images and 9
charts, and `referenceCounts` by resource kind. The JSON does not contain
reference names, Secret keys, or extra input fields. YAML errors report their
position but do not show the contents. The plan does not connect to a cluster
or registry. Python, PyYAML, and Helm must already be installed.

After the target deployment is authorized, run:

```bash
make deploy DEPLOY_CONFIG=/path/to/environment.yaml
```

This command must also have kubectl, OpenSSL, and Docker buildx for read-only
artifact inspection. Their normal authentication must be configured before you
run the command. Before its first persistent write, the command examines
release ownership, the necessary Secret key names, storage references, and
image/chart availability. It refuses to adopt unmanaged objects or resources
from another Helm release.

Before a write, the command also examines these items:

- The configured admission CA and service DNS name
- The MCP/Broker server chains
- The Broker client's CA, client purpose, and exact MCP URI identity contract
  that already exists

The ingress certificate must be valid now and must cover `domain`. This test
is not a public-browser trust test. The helper extracts only the referenced
`tls.crt` and CA fields into temporary public certificate files. Kubernetes
still returns the whole Secret for a JSONPath request. The `get Secret`
permission is necessary for this request. The helper does not parse, save, or
log private-key, cookie, or provider-key values.

If an OpenSSL test fails, the command stops before any persistent cluster
write. It does not issue or rotate certificates.

The helper installs or upgrades CetiRun Istio and waits for OAuth2 Proxy. It
reconciles only the named `oauth2-proxy` mesh provider through the
resource-version-checked helper that already exists. A provider for another
service or a replaced ConfigMap stops the deployment. Set
`istio.mode: external` only when the named release and provider are already
managed elsewhere. That mode examines them and does not upgrade the release or
patch the mesh.

Next, the Broker and its Auth SDK start in the credential namespace. Then the
helper installs the platform. It waits for every Deployment and StatefulSet,
which includes PostgreSQL, Valkey, MCP, the admission server, and the
configured Skill Marketplace. On a new installation, the admission server
starts before the fail-closed webhooks are installed. An upgrade never disables
the admission enforcement that is already in effect.

At the end, a server-side dry-run of a Team creation examines the served
admission path. This dry-run does not store a Team. If a read, render, upgrade,
rollout, or dry-run fails, the helper returns a nonzero exit status. There is
no automatic rollback or ownership transfer.

An upgrade must keep the current admission administrators, Team assignments,
Pod and strict validation settings, coordination authority, and active native
execution controls. If a control value is unreadable or has an external
reference, the upgrade stops. Changes to these protections must use a separate
approved procedure. The helper does not migrate arbitrary stored Helm values.

The Operator's `connectionsMCP.runtimeTypes`, the Portal's
`agentTools.runtimeTypes`, and the Broker's `toolService.runtimeTypes` must name
the same runtimes. A consistent explicit `[hermes]` selection is still
supported.

For Agents that already exist in a selected runtime, the helper extracts the
name, the runtime, the Team/Sandbox markers, and the necessary ServiceAccount
settings. It does not output owner details, unrelated Helm values, or
conversation data. The helper rejects an ordinary Agent if it depends on a
custom ServiceAccount, an API role, or an API token. The rejection occurs
before the MCP enablement can override those settings.

Policy-bound Sandbox Agents use the separate native path. This check does not
treat them as ordinary Agents. Kubernetes still returns complete objects for
these projections. The helper only parses the selected fields. The caller must
have permission to read these Agent, Team, Deployment, and webhook resources.
The helper does not grant itself more permissions.

### Preserve certificates and retained data

`istio.tlsMode: external` uses the `cetirun-tls` Secret that is already in the
ingress namespace. If the Istio release owns that Secret, this mode fails
before writes. The reason is that when `tls.enabled` is disabled, the release's
certificate is deleted.

If a chart-owned certificate already exists, set `istio.tlsMode: retain`. For
this mode, the release must already exist, and the Secret ownership must match.
The helper omits `tls` from the actual override file. It uses `--reuse-values`
for that Istio upgrade. Helm keeps the prior TLS values, and private-key
material does not go into the helper's override file. To change the ownership
mode, you must do a separate migration.

The offline render uses synthetic cookie/TLS values only in a separate
temporary render file. The helper never applies that manifest. Actual upgrades
use the normal charts and their server-side Secret lookup. If the upgrade
cannot find a cookie or database Secret to keep, it fails before writes. It
does not replace that Secret with new keys.

Agents, HOME/PVCs, and user keys that already exist stay in place. If you run
the command again, it reconciles the same releases. It does not disable
admission enforcement. Use a separately authorized environment test to examine
the actual cookie, certificate, and database continuity.

### Deployment readiness is not runtime qualification

Broker/MCP readiness endpoints do not show that the provider or Auth SDK APIs
are available. The helper examines the workload rollouts, the necessary
containers and Istio sidecars, the wired references, and the admission
dry-run. It does not run an OAuth sign-in, a provider token exchange, a real
model call, or a terminal session. The Gateway's own health probes use its
configured model settings. After startup, these probes can make model calls.

Native Sandbox execution and physical writer fencing are separate
qualification work. The default does not enable `sandboxSources`, and it does
not create capability registrations. A native configuration that already
exists must keep its real qualification inputs.

The deployment does not add CetiAdmin assets. It does not resolve the
coexistence of the CetiAdmin ServiceAccount/token with MCP. If an ordinary
Agent that already exists has that conflict, the deployment stops before
writes and names the dependency that is still there. The helper does not
silently remove OpenClaw from the allowlist. Instance creation requests in this
file are rejected.

## Use the component chart directly

Direct chart consumers can optionally enable CetiRun Connections MCP. Install
the Agent Broker separately in its private credential namespace. The umbrella
rejects an enabled Broker that is colocated with the Tool service. Provision
TLS and OAuth separately. Then configure these items:

- The MCP Broker HTTPS origin
- The Portal and Operator management HTTP origins
- The Operator Secret that contains only the MCP URL and CA

The sample `app/cetirun-connections-mcp/chart/ci/render-values.yaml` contains
synthetic render-only configuration. It does not create certificates or
authorize deployment.

The bundled Operator chart manages the four CetiRun CRDs as Helm templates. It
also manages their schema upgrades. Use values that explicitly confirm the
Istio prerequisites that are installed separately:

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

The final command makes sure that the Portal has the Istio sidecar that is
necessary for its authorization policies. The command also lists init
containers because Istio can inject the sidecar as a native sidecar. A native
sidecar is an init container that continues to run. The AKS Istio add-on
injects the sidecar in this way.

If you use a custom gateway, set `cetirun-operator.istioGateway` to the same
`<namespace>/<name>` as `cetirun-portal.istio.gateway`.
`cetirun-portal.istio.gatewayNamespace` and `gatewayServiceAccount` identify
the selected gateway workload. This workload can run in a namespace other than
the namespace of its Gateway resource. Also set
`cetirun-operator.cetirun.helmValues.networkPolicy.istioIngressNamespace` to
the namespace of that gateway workload. If the value is not that namespace,
the instance NetworkPolicies block ingress traffic.

Helm upgrades the CRD templates with this release. For compatible changes, a
separate CRD apply script is not necessary. The
`helm.sh/resource-policy: keep` annotation keeps CRDs and their objects during
an uninstall. It does not protect against an incompatible schema change or
downgrade.

By default, the Operator chart sets `sandboxAdmission.validationMode` to
`compatible`. This mode keeps known validation and authorization checks and
accepts newer CR fields. Refer to the
[schema and binary upgrade contract](../cetirun-operator/README.md#schema-and-binary-upgrades).
For CRDs that were previously unmanaged, and for ownership transfers across
releases, a separately reviewed handover is still necessary. This chart does
not forcibly adopt them.

For a different Gateway resource, set `cetirun-operator.istioGateway` and
`cetirun-portal.istio.gateway`. For its workload identity, set
`cetirun-portal.istio.gatewayNamespace` and `gatewayServiceAccount`, as the
command above shows. `make deploy` sets these values from its `istio` inputs:
the Gateway `cetirun-gateway` in `istio.ingressNamespace`, and
`istio.ingressServiceAccount`.

To install in a namespace other than `cetirun-system`, set both
`cetirun-operator.namespace` and `cetirun-portal.aiGateway.catalogNamespace` to
that namespace. The operator publishes the shared models in its own namespace,
and the Portal reads them from there. `make deploy` sets both values from
`platform.namespace`. The chart refuses a Portal that looks for the models in a
different namespace.

When you use the umbrella, configure the separate Istio release to reach the
co-located Portal before you install it:

```bash
PORTAL_AUTH_NAMESPACE=cetirun-system ./infra/istio/deploy.sh
```

You can still install each component chart independently. A separately planned
handover is necessary to move standalone releases that already exist into this
umbrella. The chart never adopts resources that another release owns.

## Sandbox creation profiles

Configure `cetirun-portal.sandboxRuntimeProfiles` for each supported built-in
Sandbox runtime. Each entry contains `chartVersion`, a digest-pinned `image`,
and `port` (18789 for OpenClaw, 8642 for Hermes). The chart version must match
the Operator's configured runtime chart. That chart's profile must approve the
image and port. Configure only the harnesses that the selected platform
supports. The common native executor supports OpenClaw and Hermes, but retained
single-start records are for OpenClaw only.

The Portal records these inputs in the initial Agent creation request. As a
result, users do not enter deployment coordinates in the creation form.
Admission still applies to explicit image or port requests. By default,
deployment profiles that do not exist or are not valid reject creation before
any Agent, namespace or Secret is written.

With the Operator's `sandboxSources.nativeExecution` enabled, the deployment
can set `cetirun-portal.sandboxRuntimeDefaultsFromController: true`. This lets
profiles that do not exist use trusted chart, image, and port defaults in the
common executor. Configured profiles are still validated. The platform
chart rejects this Portal opt-in unless the Operator and its native sources are
enabled.

For standalone Portal releases, make sure that the Operator configuration
matches before you enable the opt-in. For these releases, disable the opt-in
before you disable native execution. These defaults do not authorize a runtime
or change the selected AgentPolicy/Sandbox checks.

When `cetirun-operator.sandboxAdmission` is enabled, the chart adds the Portal's
service account, `system:serviceaccount:<release namespace>:cetirun-portal`, to
the admission administrators. The Portal writes Agents, Teams, Sandboxes and
AgentPolicies with this account. If the account is not an admission
administrator, the enforced admission webhook refuses its writes to
policy-bound Agents. For example, the webhook refuses the write that saves an
owner's GitHub Copilot models after a sign-in.

`cetirun-operator.sandboxAdmission.portalServiceAccount` identifies the
account. By default, the account is `cetirun-portal`. An installation that
already lists the account in `adminUsers` keeps the same admission
configuration. The Portal service account does not count as the administrator
that `adminUsers` or `adminGroups` must name. For example, reauthorization has
no Portal page.

- If you use a custom `cetirun-portal.serviceAccount.name`, set
  `portalServiceAccount` to the same name. The chart refuses different names.
- If you disable the bundled Portal, clear `portalServiceAccount`. If you do
  not clear it, the chart refuses it. List a separately installed Portal's
  service account in `adminUsers`.
- An upgrade with `--reuse-values` from an older release does not set
  `portalServiceAccount`. Set it, or list the Portal's service account in
  `adminUsers`. If you do neither, the chart refuses enforced admission.

Administrator groups cannot be blanket groups, for example
`system:authenticated` or `system:serviceaccounts:<namespace>`. The admission
server refuses to start with these groups. As a result, the chart also refuses
them.

## Skill Marketplace

The optional `cetirun-skill-marketplace` subchart syncs one Git repository of
skills. It publishes the skills that each policy-bound Team selects to a Blob
NFS container, and it creates the read-only skills claim in the namespace of
each native Sandbox Agent. It is off by default. To enable it, also enable
`cetirun-operator.skillMarketplace.enabled` and
`cetirun-portal.skillMarketplace.enabled`. The Operator chart accepts its
setting only with `sandboxSources.enabled` and `nativeExecution`, and without
`sandboxSources.skillsCatalogConfigMapName`.

The platform chart refuses the marketplace in these cases:

- The bundled Operator is disabled, its setting is off, or the setting of the
  bundled Portal is off.
- The release namespace, the marketplace's `catalog.namespace`, or the Portal's
  `skillMarketplace.catalogNamespace` is not `cetirun-operator.namespace`.
- `agentClaims.claimName` is not the Operator's
  `sandboxSources.skillsClaimName`.

`make deploy` sets these values from the optional `skillMarketplace` block of
the environment file:

```yaml
skillMarketplace:
  repository:
    url: https://git.example.com/example/skills.git
    ref: main                # Optional. Empty uses the remote HEAD.
    path: skills             # Optional. Empty is the repository root.
    auth: token              # none, token, or ssh
    existingSecret: skills-repository
    syncInterval: 5m         # Optional. At least 30s.
  storage:
    resourceGroup: example-rg
    storageAccount: examplemarketplace
    containerName: marketplace
    storageClassName: azureblob-nfs-premium
```

With `auth: ssh`, use an `ssh://` URL or the `user@host:path` form. The URL
must not contain credentials. For `auth: token`, the Secret in
`platform.namespace` has the key `token`. For `auth: ssh`, it has the keys
`ssh-privatekey` and `known_hosts`. Before its first write, the command makes
sure that these keys and the StorageClass exist.

The command refuses the block if the native Sandbox sources are off, if the
Operator uses a published skills catalog, or if the storage is the
`sharedSkills` container. It takes the image from `release`, the claim name
from the Operator's `sandboxSources.skillsClaimName`, and the catalog namespace
from `platform.namespace`. Set other chart values, for example
`repository.tokenUsername`, under `platform.values.cetirun-skill-marketplace`.

On a new installation, the bootstrap upgrade does not install the marketplace.
The command waits for its rollout after the final platform upgrade. A ready
marketplace has not necessarily synchronized the repository. The catalog
ConfigMap `cetirun-skill-marketplace` in the operator namespace shows the
synchronized commit and any sync error.

NFS 3.0 authorizes clients only by network. Use a storage account that only
private endpoints can reach, and turn off its public network access. Refer to
[Storage](../../site/content/en/docs/operations/storage.md) and the
[Skill Marketplace README](../cetirun-skill-marketplace/README.md).

## AI Gateway

Because OpenClaw and Hermes Agents take their models from the AI Gateway, the
AI Gateway is necessary for the platform. The chart refuses to install with
`cetirun-operator.aiGateway.enabled` or
`cetirun-portal.features.aiGatewayEnabled` disabled. Hermes Agents pinned to a
chart version without Hermes' Gateway provider keep their own model provider
and sign-in until their chart is upgraded.

By default, the release also installs the Gateway with its PostgreSQL and
Valkey in the release namespace. The name of the Gateway is
`cetirun-ai-gateway`. The release namespace must be the operator's namespace
because the operator reads the Gateway master key from there. Refer to the
[Gateway chart](../cetirun-ai-gateway/chart/README.md) for its settings, which
are under `cetirun-ai-gateway` in this chart.

Shared models are the models set in `cetirun-ai-gateway.models`. The Gateway
chart gives each model a deployment ID and an access group derived from its
name. The operator offers each model through its deployment ID and access
group. It offers every model to Agents without a policy, and it offers
policy-bound Agents the models that their AgentPolicy grants. If the Gateway
blocks models that do not have a price, as it does by default, each model must
also have a price. In this example, the provider key is in a Secret that you
create in the release namespace:

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

The access group `cetirun-shared` is no longer necessary for these models. A
model that has it continues to work. When the settings of a model change, for
example its endpoint, provider parameters or price, the model keeps its access
group. As a result, Agents continue to use the model and are not restarted for
this change.

Also, an upgrade of the Gateway does not interrupt the Agents. The Gateway pods
keep the configuration that they started with until they are replaced. The
operator applies a changed model list after the rollout ends.

Shared models are optional. Without shared models, OpenClaw Agents use their
owners' GitHub Copilot. With `cetirun-operator.aiGateway.copilotProxy.enabled`
disabled, the Portal creates no OpenClaw Agent until a shared model is offered.
The Gateway's health checks do not probe the Agents' GitHub Copilot routes
because each probe is a real call on the owner's Copilot allowance.
`cetirun-operator.aiGateway.copilotProxy.healthChecks.enabled` enables their
probes.

A model added in the console is shared only if it has the access group
`cetirun-shared` and a price. It is shared only with Agents without a policy.
To open the console, use these commands:

```bash
kubectl get secret cetirun-ai-gateway-auth -n cetirun-system \
  -o jsonpath='{.data.ui-password}' | base64 -d; echo
kubectl port-forward -n cetirun-system svc/cetirun-ai-gateway 4000:4000
```

Then open <http://localhost:4000/ui> and sign in as `admin`.

You can use a separately installed Gateway instead. To do this, set
`cetirun-ai-gateway.enabled=false`. Install the Gateway in the operator
namespace under the name `cetirun-ai-gateway`, with
`operatorIntegration: true`. If you install the Gateway elsewhere, set
`cetirun-operator.aiGateway.url` to the URL of that Gateway. In this case, also
copy its master key into the operator namespace as the
`cetirun-ai-gateway-auth` Secret.

New Agents must have a Team. Before users create Agents, create a Team in the
Portal's administration pages or with a manifest:

```bash
kubectl apply -f - <<'EOF'
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Team
metadata:
  name: example-team
spec: {}
EOF
```
