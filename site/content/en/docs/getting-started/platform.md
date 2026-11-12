---
title: "Install CetiRun"
description: "Prepare AKS, identity, networking, and storage, and then install the CetiRun platform with make deploy."
weight: 30
---

`make deploy` installs CetiRun on an AKS cluster that you prepare. It installs
three Helm releases:

- The CetiRun Istio release: OAuth2 Proxy, the shared Gateway, and the sign-in
  and ownership filters
- The Agent Broker, in its own credential namespace
- The platform release, in `cetirun-system` by default: the Operator, the
  Sandbox admission server, the Portal, the AI Gateway, Connections MCP, and
  the ADX-Mon collection. The AI Gateway includes PostgreSQL and Valkey. By
  default, the Operator also deploys CetiAdmin, the Hermes administration
  Agent.

Use `make deploy` to install CetiRun. The component charts are also available
for development and standalone use. Refer to
[Install without make deploy](#install-without-make-deploy).

> **Warning:** Before you run the deployment scripts, review the scripts,
> target subscription, Kubernetes context, costs, and access policies. The
> scripts create or update cloud and cluster resources.

The [installation checklist](../../operations/install-checklist/) shows all
steps on one page, with the result of each step and how to verify it. To
validate the charts locally without a deployment of resources, follow
[Run local checks](../local-checks/).

To run only the AI Gateway for agents that you operate yourself, install its
chart on its own instead. The
[Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#install)
tells you how to publish its image and chart and how to install it. It also
covers smart routing of models and the first call from a client.

## Prepare the environment

### Cluster requirements

| Requirement | Why it matters |
| --- | --- |
| Kubernetes 1.37 or later | `make deploy` refuses an older version. Sandbox admission must have this version |
| AKS with managed Istio and an external ingress gateway | Shared authentication and instance routes |
| Managed Istio traffic redirection set to `InitContainers` | Agents run in Kata VM-isolated Pods. With CNI chaining, the `istio-validation` init container of these Pods fails, and the Agent does not start |
| OIDC issuer and Workload Identity | Federated identity for OAuth2 Proxy and Agent Identity |
| Azure Disk CSI and the `default` StorageClass | Default persistent HOME volumes |
| Blob CSI, a Blob NFS StorageClass such as `azureblob-nfs-premium`, and a Blob NFS storage account and container | Shared skills volumes. They are necessary for `make deploy`. Refer to [Shared skills storage for `make deploy`](../../operations/storage/#shared-skills-storage-for-make-deploy) |
| A compatible `kata-vm-isolation` RuntimeClass | The Agent chart sets `runtimeClassName: kata-vm-isolation`. Keep this value unless you qualified another isolated runtime |
| Kata node pools and Azure CNI powered by Cilium | Agents of Teams created in the portal run in a Sandbox. Refer to [Policy-bound Teams](../../operations/policy-teams/) |
| Enforced Kubernetes NetworkPolicy and necessary admission APIs | Runtime isolation rules and the default exec-denial policy |
| Accessible images and versioned OCI agent chart | Operator fetches the agent chart, and then nodes pull images |
| Entra ID app, allowed groups, federated credential | Portal sign-in and group-based sign-in access |
| User names that give a valid Portal alias | The Portal refuses other users. Refer to [User names](#user-names) |
| Apex DNS and a TLS certificate that matches it. Wildcard names if you use them | Portal, instance routes, and apex `/oauth2/callback`. Optional `publicTLS` in [Public DNS and TLS](../../operations/public-ingress/) can prepare one exact hostname during `make deploy` |

Each Agent release creates a `ValidatingAdmissionPolicy` that denies
`kubectl exec`. As a result, the
Operator must be allowed to create cluster-scoped admission policies. To see
the admission and Istio APIs of the cluster, run these commands:

```bash
kubectl api-resources --api-group=admissionregistration.k8s.io
kubectl api-resources --api-group=networking.istio.io
kubectl api-resources --api-group=security.istio.io
```

The first list must contain `validatingadmissionpolicies` and
`validatingadmissionpolicybindings`. The second list must contain `gateways`,
`virtualservices`, and `sidecars`. The third list must contain
`authorizationpolicies` and `peerauthentications`. A successful render does not
show that a Kubernetes distribution is supported.

The [cluster template]({{< repo "infra/cluster/modules/aks-cluster.bicep" >}})
sets the managed Istio traffic redirection to `InitContainers`. If you use a
cluster that the template did not create, read the setting with the API
version of the template:

```bash
az resource show --resource-group example-rg --name example-cluster \
  --resource-type Microsoft.ContainerService/managedClusters \
  --api-version 2026-01-01 \
  --query properties.serviceMeshProfile.istio.components.proxyRedirectionMechanism \
  --output tsv
```

If the output is not `InitContainers`, set this property of the cluster before
you install CetiRun.

Sources: [runtime values]({{< repo "app/cetirun-chart/values.yaml" >}}),
[exec policy]({{< repo "app/cetirun-chart/templates/deny-exec-policy.yaml" >}}),
and [operator RBAC]({{< repo "app/cetirun-operator/chart/templates/clusterrole.yaml" >}}).
The chart does not create the RuntimeClass, CSI driver, or Istio control plane.

### User names

The Portal accepts only users whose user name gives a valid alias. The Portal
takes the part of `preferred_username` before `@` and changes it to lowercase.
If the token has no `preferred_username`, the Portal uses the email address.
The alias must be a DNS label:

- 1 to 63 characters
- Only letters, digits, and hyphens (`-`)
- A letter at the start, and a letter or a digit at the end

The Portal does not replace characters. For other users, for example
`jane.doe@example.com` or `jane_doe@example.com`, it returns HTTP 403
`invalid_identity`. `ownerAlias`, the aliases in
`platform.values.cetirun-portal.admins.aliases`, and the CetiAdmin
`owner.alias` must use the same format.

### Tools

Run `make deploy` on a Linux or macOS workstation. The scripts use Bash and
POSIX file locks. The cluster helper also needs Bash 4.4 or later, and the
storage helper needs GNU grep as `grep`. macOS has Bash 3.2 and BSD grep by
default.

| Tool | Version | Use |
| --- | --- | --- |
| Python with PyYAML | Python 3.9 or later. CI tests use Python 3.12 and PyYAML 6.0.3 | `make deploy` and the helpers |
| Helm | Helm 3. CI tests use Helm 3.22.0 | Renders and installs the charts |
| kubectl | No more than one minor version from the cluster | `make deploy` stops if the versions are farther apart |
| kubelogin | A version that `az aks install-cli` installs | Entra sign-in for kubectl. Refer to [Connect to the cluster](../../operations/infrastructure/#connect-to-the-cluster) |
| OpenSSL | The examples of the identity pages use OpenSSL 3 | Certificate checks before `make deploy` writes |
| Docker with Buildx | A version that supports `docker buildx imagetools inspect` | `make deploy` reads the image manifests. `make publish` builds the images |
| Azure CLI with Bicep | CI tests use Bicep 0.47.16 | The cluster and storage helpers, and Azure DNS for `publicTLS` |
| jq | Any current version | Examples on the identity and Istio pages |

Some systems do not let `pip` install packages into the system Python. On
these systems, install PyYAML in a virtual environment outside the repository.
Then activate the environment before you run `make`:

```bash
python3 -m venv /absolute/private/cetirun-venv
. /absolute/private/cetirun-venv/bin/activate
python -m pip install --requirement infra/istio/requirements.txt
```

### Permissions and quota

Each step needs its own permissions. Entra directory roles and Azure roles are
different permissions.

| Step | Permission | Scope |
| --- | --- | --- |
| Cluster helper | Register the `ManagedGatewayAPIPreview` feature and the `Microsoft.ContainerService` and `Microsoft.Kusto` resource providers. Create resource groups | Subscription |
| Cluster helper | Deploy AKS and the Azure Data Explorer resources of the ADX-Mon collection, for example as Contributor | Cluster resource group, and `adxResourceGroupName` if it names another resource group |
| Cluster helper | Write to `kube-system`, for example with **Azure Kubernetes Service RBAC Cluster Admin**. Assign this role before the helper runs | Cluster resource group, because the cluster does not exist yet. Refer to [Helper side effects](../../operations/infrastructure/#helper-side-effects) |
| Storage helper | Update the service endpoints of the node subnet. Deploy the storage account | The node subnet, and the storage resource group |
| kubectl, Helm, and `make deploy` | Create cluster-scoped objects, namespaces, and Secrets, for example with **Azure Kubernetes Service RBAC Cluster Admin** | Cluster |
| Registry | Push images and charts. Assign pull roles to the cluster and the Operator | Registry. Refer to [Publish images and charts](../../operations/artifacts/) |
| Portal sign-in | Register an application, assign groups, add a federated credential, and grant admin consent, for example as Cloud Application Administrator | Entra tenant. Refer to [Istio authentication](../../operations/istio/#prepare-identity-dns-and-tls) |
| Agent Identity | Create the Blueprint, the Authorization application, and their federated credentials | Entra tenant. Refer to [Prepare Microsoft Entra Agent Identity](../../operations/entra-agent-identity/) |
| Encryption key | Create the Key Vault key and assign its roles | Key Vault. Refer to [Prepare Agent connections](../../operations/agent-identity/) |

The default node pools need 352 vCPUs of the Dsv6 family at their minimum
counts. Before you deploy, compare this with the vCPU quota of the region, as
[Check the vCPU quota](../../operations/capacity/#check-the-vcpu-quota) shows.

### Environments and namespaces

A cluster holds one CetiRun installation. The four CetiRun CRDs are
cluster-scoped, and the admission webhooks have fixed names. `make deploy`
refuses to change admission configuration that another release owns. For each
environment, for example test and production, use a separate cluster, domain,
and environment file. Also use separate Entra applications, so that a change
for one environment does not affect another.

Keep `platform.namespace` at its default, `cetirun-system`. The Portal reads
the Operator Deployment at the fixed name `cetirun-system/cetirun-operator`. In
another namespace, the Portal **Upgrade to Latest** action does not work, and
the Portal does not show the access links of Custom Agents.

## 1. Prepare the cluster

[Infrastructure](../../operations/infrastructure/) covers the
Bicep inputs and helper side effects. The cluster and storage helpers expect
`.bicepparam` files that you write. The repository does not include these
files. Set `PARAM_FILE` explicitly to your reviewed file.

The cluster helper installs or updates an Azure CLI extension, registers a
preview feature, provisions infrastructure, enables Gateway API, and overwrites
the selected kubeconfig entry. Node-pool defaults are not sized for a small
demonstration environment.

`make deploy` needs a Kusto cluster with the logs, metrics, and Agent logs
databases of the ADX-Mon collection. It also needs the ingestor's managed
identity, with its roles and federated credential. By default, the cluster
template creates them. Its outputs `adxClusterUri` and `adxIngestorClientId`
give `observability.adx.endpoint` and `observability.ingestorClientId` of the
environment file. With `adxContent`, the template also creates the database of
the AI Gateway's prompts and responses: its outputs `adxContentDatabase` and
`adxContentClientId` give the optional `observability.content` block. Refer to
the prerequisites in
[Monitoring and logs](../../operations/observability/#prerequisites).

## 2. Configure authentication and storage

Follow [Istio authentication](../../operations/istio/) and
[persistent storage](../../operations/storage/). On the Istio page, do only
the identity, DNS, and certificate steps. `make deploy` installs and upgrades
the CetiRun Istio release. Do not also run `infra/istio/deploy.sh` or a direct
Helm installation of the Istio chart on the same cluster.

The identity redirect URI is `https://<domain>/oauth2/callback`. Portal and
native-instance routes use paths on the apex domain. The Gateway also accepts wildcard hosts, but
that does not create a VirtualService for each subdomain. Configure DNS and
certificates for the hosts that you serve. A wildcard certificate alone does
not cover the apex.

The Gateway refers to `cetirun-tls` in its configured namespace
(`aks-istio-ingress` by default). The setting `tls.enabled: false` disables
only chart-managed Secret creation, not TLS. Configure the `oauth2-proxy`
external authorization provider in the managed Istio shared ConfigMap. A direct
Helm installation does not do that patch.

For a single exact hostname, configure `publicTLS` in the same environment file
before deployment. `make deploy` can configure Azure Public IP DNS or validate
a user-managed record. Then it issues the Gateway certificate with cert-manager
HTTP-01 before it deploys the complete platform. The normal environment file
still needs all identity, Broker, admission, model and runtime configuration.

Do not expose the portal before you make sure that authentication, ownership
checks, and namespace sidecar injection work. The shared filter calls the
Portal ownership endpoint on port 8081. With `make deploy`, this call uses mesh
mTLS, and the Portal accepts only the service account of the ingress gateway.
A NetworkPolicy also restricts the port.

Before the first deployment, also review these settings:

- **Shared skills:** `make deploy` needs shared skills storage, which is
  separate from instance home PVCs. Before the first run, provision it and set
  the `sharedSkills` fields, as
  [Shared skills storage for `make deploy`](../../operations/storage/#shared-skills-storage-for-make-deploy)
  shows.
- **Internal network exceptions:** review `networkPolicy.allowedInternalCIDRs`
  rather than inherit its environment-specific exception.
- **npm registry:** runtime npm skills install from the public npm registry.
  To use your approved registry, refer to
  [Shared skills mounts](../../operations/storage/#shared-skills-mounts).
- **Outbound traffic:** Istio ServiceEntries are not an outbound allowlist. To
  restrict outbound traffic, configure AgentPolicy network controls.
- **Old installations:** if an installation used the old shared blacklist,
  follow the
  [retired-filter cleanup procedure](../../operations/istio/#retired-shared-blacklist)
  when you upgrade it.

## 3. Prepare Agent Identity and admission

`make deploy` installs the platform with Agent connections and Agent Identity.
Before you run it, complete the steps of
[Prepare Agent connections](../../operations/agent-identity/).
It covers the separate Broker, Key Vault, MCP certificates and trust, provider
registrations, and Entra lifecycle configuration. `make deploy` does not
provision these external resources.

`make deploy` installs the Broker with Microsoft delegated access, and it
enables the Agent Identity lifecycle of the operator. If the environment file
disables one of these features, the run stops. As a result, the Entra parts of
that setup are not optional with `make deploy`. These parts are the Agent
Identity Blueprint, the Authorization application, the encryption identity, and
the Key Vault key. GitHub and Google connections stay optional. Refer to
[What `make deploy` requires](../../operations/agent-identity/#what-make-deploy-requires).

Also prepare these items:

- A client ID for `platform.values.cetirun-operator.azure.clientId`. Use the
  client ID of the Agent Identity Blueprint, unless another operator
  integration must use a different identity. `make deploy` stops if the value is
  empty. Refer to
  [Federate the AKS workloads to the Blueprint](../../operations/entra-agent-identity/#federate-the-aks-workloads-to-the-blueprint).
- The Sandbox admission certificate. Refer to
  [the admission certificate](../../operations/policy-teams/#the-admission-certificate).

`make deploy` reads the Secrets in the cluster before it writes. It does not
examine the Entra applications or the Key Vault key.

## 4. Publish the images and charts {#publish-the-images-and-charts}

Build the images and charts from the revision that you install, and push them
to a registry that the cluster can read. [Publish images and charts](../../operations/artifacts/)
gives the commands, the registry access for the nodes and the Operator, and an
Azure Container Registry example. It also lists the images of other projects
that a private registry must hold.

Run `make deploy` from the same commit that built the artifacts, and set the
`release` section and the approved Custom image digests from that build. Refer
to [Use the artifacts with make deploy](../../operations/artifacts/#use-the-artifacts-with-make-deploy).

## 5. Create the AI Gateway Secrets

The AI Gateway reads the provider keys of its models from a Secret. It also
uses a Secret with its master key, salt key, and console password, and a
Secret with the password of its Valkey. Create the namespace and these Secrets
in the namespace of `platform.namespace`. The commands use the default
namespace, `cetirun-system`.

The commands use the current context of your kubeconfig. First make sure that
`kubectl config current-context` shows the value of `target.context`. Refer to
[Connect to the cluster](../../operations/infrastructure/#connect-to-the-cluster).

Do not type a provider key on a command line, because the shell history keeps
the command. Write the keys in an environment file outside the repository, with
one `NAME=value` line for each key. Use a text editor, for example:

```text
AZURE_OPENAI_KEY=<key>
AZURE_DECISION_KEY=<key>
```

Create the file with private permissions, for example after `umask 077`. Then
create the Secrets. Give the master key the `sk-` prefix, as the chart does
when it generates one:

```bash
kubectl get namespace cetirun-system >/dev/null 2>&1 ||
  kubectl create namespace cetirun-system
kubectl create secret generic gateway-provider-keys -n cetirun-system \
  --from-env-file=/absolute/private/gateway-provider-keys.env
kubectl create secret generic cetirun-ai-gateway-auth -n cetirun-system \
  --from-literal=master-key="sk-$(openssl rand -hex 24)" \
  --from-literal=salt-key="$(openssl rand -hex 32)" \
  --from-literal=ui-password="$(openssl rand -hex 16)"
kubectl create secret generic gateway-valkey-auth -n cetirun-system \
  --from-literal=redis-password="$(openssl rand -hex 24)"
rm /absolute/private/gateway-provider-keys.env
```

The shell history keeps only the `openssl` commands, not the values that they
generate.

Do not change the salt key later. If it changes, the models that were added in
the Gateway's console disappear. Keep a copy of this Secret in protected
storage. Refer to [Make and restore backups](../../operations/backup-restore/) and
to the Gateway chart's
[Credentials]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#credentials).

Each `api_key: os.environ/<NAME>` in the models reads the key `<NAME>` of the
provider-key Secret. The example environment file uses `AZURE_OPENAI_KEY` for
the chat models and `AZURE_DECISION_KEY` for `decision-1`. If you rename a key,
rename it in both places.

### The Decision-1 deployment

The default `modelRouting.mode: auto` needs the Microsoft-Decision-1
deployment that the smart-routing model `auto` uses to classify requests.
Deploy the model in an Azure AI Foundry resource, not in an Azure OpenAI
resource. Record the deployment name, the endpoint of the resource, for example
`https://<resource>.services.ai.azure.com`, and its key. Refer to
[Foundry Models sold by Azure](https://learn.microsoft.com/azure/foundry/foundry-models/concepts/models-sold-directly-by-azure).

A subscription can have no quota for this model. Before you deploy it, find
the entries of Microsoft-Decision-1 in the output of
`az cognitiveservices usage list --location <region>`. If their limit is 0,
request more quota. To see if a region offers the model, use
`az cognitiveservices model list --location <region>`.

With the default `modelRouting.mode: auto`, `make deploy` requires the
`decision-1` and `auto` entries, but it does not call the deployment. If the
deployment does not work, `auto` sends each request to its default model. Each
request to `auto` can cause a classifier call of up to 32,768 tokens. Size the
capacity of the deployment for this traffic. Refer to
[Choose a classifier](../../operations/smart-routing/#choose-a-classifier).

To deploy before classifier quota is available, set `modelRouting.mode: direct`
in the environment file. In this mode, `make deploy` refuses `auto`,
`decision-1`, other classifier or router entries, and models that are not
conversation models, for example embedding models. Configure callable shared
models without them. Set enabled CetiAdmin's `model.default` to one of
the configured models. All other deployment checks stay in effect.

In direct mode, the AI Gateway has no router and no decision model. Team
routers with the `heuristic` or `llm` classifier still work, because they use
only shared chat models. A `jev` classifier needs a decision model, so it does
not work in this mode.

## 6. Write the environment file

Write the environment file outside the repository. Start from the
[synthetic example]({{< repo "app/cetirun-platform/ci/deployment-example.yaml" >}}),
and replace every value in it. The example uses the Secret names of this page
and of the pages that the steps above link to. If you use other names, change
them in the file. The
[platform chart README]({{< repo "app/cetirun-platform/README.md" >}}#deploy-the-platform)
describes each input of the environment file.

Put the Helm values of each release under its `values` field, for
example `platform.values.cetirun-ai-gateway.models`. `make deploy` sets some
values itself, for example the registry, the version, the namespaces, the
domain, and the Istio Gateway. Do not set these values in the file, because a
different value stops the run. [Values that make deploy sets](#values-that-make-deploy-sets)
lists them.

The `istio` section names the managed Istio resources that already exist. AKS
adds the Istio revision to the names of the control plane Deployment, the
ingress gateway Deployment, and the shared mesh ConfigMap. The name of the
ingress gateway ServiceAccount has no revision. Read the revision and the names
from the cluster:

```bash
kubectl get pods -n aks-istio-system -l app=istiod \
  -o jsonpath='{.items[0].metadata.labels.istio\.io/rev}'
kubectl get deployments,configmaps -n aks-istio-system
kubectl get deployments,serviceaccounts -n aks-istio-ingress
```

| Input | Name on AKS |
| --- | --- |
| `istio.revision` | The revision, for example `asm-1-30` |
| `istio.controlPlaneDeployment` | `istiod-<revision>` |
| `istio.ingressDeployment` | `aks-istio-ingressgateway-external-<revision>` |
| `istio.ingressServiceAccount` | `aks-istio-ingressgateway-external` |
| `istio.meshConfigMap` | `istio-shared-configmap-<revision>`. With `istio.mode: manage`, `make deploy` creates it if it does not exist |

On a new cluster, `istio-shared-configmap-<revision>` does not exist yet. The
listing of `aks-istio-system` shows the ConfigMap `istio-<revision>` instead.
AKS manages that ConfigMap. Do not put its name in `istio.meshConfigMap`.

Before it writes, `make deploy` makes sure that the two Deployments exist. If a
Deployment does not exist, the run stops and names the input. If the name with
the revision exists, the message also gives that name. The run also stops if
`istio.meshConfigMap` does not exist but `istio-shared-configmap-<revision>`
exists.

### Inputs to decide before the first run

Some inputs are difficult to change after the first run. Make a decision about
these inputs before the first run:

- `ownerAlias` is the portal administrator. Portal administrators create Teams,
  Agent Policies, and Sandboxes. To add other administrators, list them in
  `platform.values.cetirun-portal.admins.aliases`.
- `identityRealm` names the authentication realm of Portal Chat history, for
  example `entra:<tenant-id>`. Keep it stable. A new value selects a different
  history partition in each browser, and users do not see their earlier
  history. Refer to [Custom chat](../../guides/custom-chat/).
- `istio.values.allowedGroups` lists the object IDs of the Entra groups that
  can sign in, separated by commas. These are the groups that you assign to
  the Portal sign-in application. Refer to
  [Istio authentication](../../operations/istio/#prepare-identity-dns-and-tls).
- `platform.values.cetirun-operator.sandboxAdmission` names the admission
  certificate. It also names the administrators who can write policy-bound
  configuration with `kubectl`. A later run cannot change these administrators.
  Use an Entra group in `adminGroups`, so that a change of staff needs only a
  change of group membership. To read the names and groups that Kubernetes
  receives, use `kubectl auth whoami`. Refer to
  [the admission certificate](../../operations/policy-teams/#the-admission-certificate).
- `platform.values.cetirun-operator.cetiadmin` configures CetiAdmin, the Hermes
  operations Agent that the operator deploys and that `make deploy` enables by
  default. CetiAdmin can read the Secrets of each standard Agent and run
  commands in its Pods. Set its owner, its initial monthly budget, and its
  access to the Kubernetes API, or set `enabled: false`. Its `owner.alias` must
  be the same as `ownerAlias`. The default model of CetiAdmin is `auto`. Before
  you set these values, read [CetiAdmin](../../operations/cetiadmin/).
- `cetiadmin.kubernetes.apiAudience` is the token audience that CetiAdmin uses
  for the Kubernetes API. The API server must accept it, and it must be
  different from the Connections MCP audience. A token that names no audience
  shows the audiences that the API server accepts:

  ```bash
  kubectl create token default --namespace default --duration 10m |
    cut -d. -f2 | python3 -c 'import base64, json, sys; p = sys.stdin.read().strip(); print(json.loads(base64.urlsafe_b64decode(p + "=" * (-len(p) % 4)))["aud"])'
  ```

- `cetiadmin.kubernetes.apiEgressCIDRs` lists the API server addresses that
  CetiAdmin Pods connect to after Service address translation. Each entry is
  one `/32` or `/128` address. To read the addresses, run this command, and add
  `/32` to each IPv4 address:

  ```bash
  kubectl get endpointslices -n default -l kubernetes.io/service-name=kubernetes \
    -o jsonpath='{.items[*].endpoints[*].addresses[*]}'
  ```

  If these addresses change, update the list and run `make deploy` again.
- `platform.values.cetirun-operator.aiGateway.defaultBudget` sets the initial
  monthly Gateway budget of each standard Agent, 10000 USD by default. No Agent
  Policy limits these Agents. Refer to
  [Policy-bound Teams](../../operations/policy-teams/).
- `platform.values.cetirun-ai-gateway.models` holds the shared models. It must
  contain callable models. The default `modelRouting.mode: auto` also requires
  the smart-routing model `auto`, which uses the decision model
  `decision-1` as its classifier. The `base_model` of `decision-1` must be
  `azure_ai/Microsoft-Decision-1`. Refer to
  [Smart routing](../../operations/smart-routing/).
- `platform.values.cetirun-ai-gateway.postgresql.bundled.persistence` sets the
  StorageClass and the size of the Gateway's database. A later run cannot
  change them. `make deploy` always installs the Gateway with its bundled
  PostgreSQL and Valkey. It refuses `bundled.enabled: false` for either of
  them. For managed databases, install the platform chart directly. Refer to
  the Gateway chart's
  [Bundled or your own]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#bundled-or-your-own).
- `platform.values.cetirun-ai-gateway.resources` reserves CPU and memory for
  each Gateway pod. The chart reserves none. Set it, and the resources of the
  bundled PostgreSQL and Valkey, before you serve users. Refer to the Gateway
  chart's [Resources]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#resources).
- LiteLLM's spend log in the Gateway's database keeps the prompt and the
  response of each call. By default, it keeps them for seven days. To keep
  only the metadata of each call, set
  `platform.values.cetirun-ai-gateway.spendLogs.storePrompts: false`. Refer to
  [the platform values](../../reference/helm/#platform-chart). To keep them in
  Azure Data Explorer instead of the spend log, set
  `platform.values.cetirun-ai-gateway.content`, as
  [Content in Azure Data Explorer]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#content-in-azure-data-explorer)
  shows.
- `platform.values.cetirun-portal.features.maxInstancesPerUser` limits the
  Agents of each user. The default is `1`, and `0` removes the limit. The
  limit also applies to Portal administrators. Because CetiAdmin counts as an
  Agent of its owner, the owner cannot create another Agent with the default
  value.

## 7. Run the plan and the deployment {#platform-chart}

`make deploy` runs `scripts/cetirun-platform-deploy.sh` with one environment
file. Run the offline plan first. Then run the deployment:

```bash
make deploy-plan DEPLOY_CONFIG=/absolute/path/environment.yaml
make deploy DEPLOY_CONFIG=/absolute/path/environment.yaml
```

The plan validates the inputs and renders the charts locally. It does not
connect to the cluster or to the registry. If a chart refuses its values, the
plan stops and shows the message of the chart. An example is a router tier that
is not in `models`.

Before it writes, `make deploy` examines the target cluster, the references,
the certificates, and the published artifacts. If an object that the
environment file names does not exist, the run stops before it writes. With
`publicTLS`, it first prepares DNS, cert-manager, and the certificates through
a restricted platform chart revision. Then it runs the full preflight again.

`make deploy` installs the three normal releases and waits for their workloads. On a new
installation, it starts the admission server before it enforces admission. At
the end, it does a server-side dry run of a Team. It does not create Agents or
send a request to a model.

`make deploy` does not call Microsoft Graph, and it calls Azure only for the
Azure DNS mode of `publicTLS`. As a result, it does not find a wrong Entra ID, a
missing Key Vault key or Kusto database, or an unusable Decision-1 deployment.
These problems occur only when a component uses the resource.

If a step fails, `make deploy` stops. The message names the command of that
step, but it does not show the output of the tool, because that output can
contain rendered Secrets. Refer to
[When make deploy fails](../../operations/troubleshooting/#when-make-deploy-fails).

`make deploy` does not install managed Istio, external credentials, Agents, or
Agent data that is kept. When `publicTLS` is enabled, it can install the
pinned cert-manager controller and issue the one configured public
certificate. The Operator subchart creates the Registry source namespace, which
is kept on uninstall. Portal and Operator manage its per-Agent credentials at
runtime.

With `istio.mode: external`, `make deploy` does not install or upgrade the
CetiRun Istio release. The release and every rendered non-Secret Istio object
must already exist with the expected Helm ownership. Its Gateway, EnvoyFilter,
AuthorizationPolicy, and other routing objects must be equal to the objects
that `make deploy` renders. If they are not equal, the run stops. A release that
`infra/istio/deploy.sh` installs is not equal, even with
`PORTAL_AUTH_NAMESPACE=cetirun-system`, because the helper does not enable
Portal mTLS. Refer to
[portal ownership endpoint](../../operations/istio/#portal-ownership-endpoint).

When upgrading from a chart without the OAuth2 Proxy PodDisruptionBudget,
upgrade the external release first, or set
`istio.values.oauth2Proxy.podDisruptionBudget.enabled: false` in the environment
file. Otherwise the missing budget stops preflight before any write. Keep the
configured replica count consistent with the external release. Refer to
[authentication availability](../../operations/istio/#authentication-availability-during-node-maintenance)
for the budget, placement, and upgrade constraints.

## 8. Verify the deployment

If you configured `publicTLS`, first run the read-only public check:

```bash
make deploy-verify DEPLOY_CONFIG=/absolute/path/environment.yaml
```

It examines the public endpoint without writing resources. Refer to
[Public DNS and TLS](../../operations/public-ingress/#deploy-and-verify). For a
certificate that you supply, there is no similar command.

`make deploy` already waited for every workload of the three releases. Make
sure that the releases are deployed, and that the cluster still has the
RuntimeClass, StorageClass, and CSI driver of the Agents:

```bash
helm list --all-namespaces
kubectl get runtimeclass/kata-vm-isolation storageclass/default csidriver/disk.csi.azure.com
```

The Istio, Broker, and platform releases must show the status `deployed`.

`make deploy` sends no request to a model. Send one to the AI Gateway with its
master key: list the models, and ask a configured model a question. The plan
reports the selected mode in `modelRouting.mode`. For `auto` mode, use the
request below. For `direct` mode, replace `"model": "auto"` with a configured
model name, such as `"model": "gpt-main"`. These commands use the default
namespace and do not print the key:

```bash
kubectl port-forward -n cetirun-system svc/cetirun-ai-gateway 4000:4000 &
MASTER_KEY="$(kubectl get secret cetirun-ai-gateway-auth -n cetirun-system \
  -o jsonpath='{.data.master-key}' | base64 -d)"
curl -s http://localhost:4000/v1/models -H "Authorization: Bearer ${MASTER_KEY}"
curl -si http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer ${MASTER_KEY}" -H 'Content-Type: application/json' \
  -d '{"model": "auto", "messages": [{"role": "user", "content": "What is 2+2?"}]}'
```

The list contains the names of `models`. In direct mode, verify that the
selected model returns a normal response; there is no classifier or routing
header to verify.

In auto mode, Agents do not see `decision-1`, because the Gateway lists it only
for keys that the operator did not issue. The answer from `auto` has the header
`x-litellm-complexity-router-tier`, and `x-litellm-complexity-router-cause` is
`jev_classifier`. Any other cause shows that the classifier did not decide.
Then examine the `decision-1` entry, its key, and the Gateway's log. Stop the
port-forward after the test.

`make deploy` enables shared skills. Also examine the StorageClass in
`sharedSkills.storageClassName` and the `blob.csi.azure.com` CSI driver. Blob
CSI is not necessary for HOME storage.

### Test a first Agent

Use two test accounts in the allowed groups. Do not use the owner account,
unless you raised `features.maxInstancesPerUser`. Refer to
[Inputs to decide before the first run](#inputs-to-decide-before-the-first-run).

| Test | Expected result |
| --- | --- |
| Open `https://<domain>/` with the first account | Microsoft sign-in, then the Portal |
| Create an Agent in the Portal | `kubectl get agents.cetirun.microsoft.com <agent>` shows the phase `Ready` |
| Read the `Ready` and `IngressReady` conditions of the Agent | Both are `True`. `Ready` alone does not show that the route works |
| Open the Agent UI | The UI loads |
| Open `https://<domain>/ui/<agent>/` with the second account | `403` |
| Write a file in HOME, then select **Restart Agent** | The file is still in HOME after the restart |
| Select **Sign out**, and then open the Portal again | A redirect through Microsoft sign-in, then the Portal. **Sign out** ends only the Portal session, so Entra can sign the user in without a prompt |
| Connect a provider account, if you registered a provider | The connection shows in the Portal |

Examine pod sidecar injection as well as namespace labels. Review the
[same-origin sign-out checks](../../operations/istio/#prepare-identity-dns-and-tls).
Test both successful and denied requests.

Before you enable Tailscale, shared storage, and cloud log collection for
users, test them separately.

## 9. Give users access

1. Add the users to an allowed group. The group must be assigned to the Portal
   sign-in application.
2. Make sure that the user names in the allowed groups give valid aliases, as
   [User names](#user-names) describes.
3. Then give users the portal address and the
   [first-instance guide](../first-instance/).
4. Tell users the number of Agents that each user can create.
5. Tell users the Agent types that they can create. `make deploy` allows
   Hermes, OpenClaw, and Custom Agents.

A Team is optional for a new agent. An agent without a Team can use every
shared conversational model, including `auto` when automatic routing is
configured. No Agent Policy applies to it, and every signed-in user can select
any Team.

An administrator creates Teams in the portal's administration pages, with an
Agent Policy and a Sandbox. The agents of such a Team run in its Sandbox, after
you configure [Policy-bound Teams](../../operations/policy-teams/). A Team can
also give its agents its own smart-routing models. To configure them, continue with
[Smart routing](../../operations/smart-routing/).

## Change the deployment

Run `make deploy` again after each change to the environment file. A run does
not change the admission administrators, and it does not remove the Sandbox
settings that are in effect. If the file tries to do this, the run stops. A
run that changes the CetiAdmin configuration, for example an upgrade, needs
the identity of an admission administrator. Refer to
[Before you upgrade](../../operations/upgrade/#before-you-upgrade).
Sandbox execution stays disabled until you enable it, as
[Policy-bound Teams](../../operations/policy-teams/) shows. The
[Skill Marketplace](../../operations/skill-marketplace/) is optional. A later
`make deploy` run enables it, together with Sandbox execution or after it.

A change to the models replaces the Gateway's Pods. When a shared model is
added or removed, or when its limits change, the Agents that can use that model
restart one time. Before you remove a shared model, remove its name from each
Agent Policy that allows it. Otherwise, the operator stops the Agents of that
Agent Policy. Refer to
[Choose a model](../../guides/models/#runtime-configuration).

To upgrade CetiRun to a new version, follow
[Upgrade CetiRun](../../operations/upgrade/).

### Upgrading runtime registry configuration

Operator-managed Agents reject raw `imagePullSecrets` overrides. This also
applies to an empty list. Before you upgrade, remove
`cetirun.helmValues.imagePullSecrets` from the Operator's saved values
(`cetirun-operator.cetirun.helmValues.imagePullSecrets` in the platform chart)
and any instance `spec.helm.values.imagePullSecrets`.

If you keep the deployment-level key, built-in Agents report
`AgentConfigInvalid` or `RegistryRuntimeConfigRejected`, even when their images
are public. Helm `--reuse-values` keeps that key. Replace the saved values with
a reviewed file that omits it.

While you migrate private images, keep a node image-pull identity that works,
or arrange a maintenance window. After you upgrade the Operator, Portal, and
CRDs, upgrade the pinned runtime chart of each affected Agent. Use a version
that advertises managed-registry support. Then configure the per-Agent
credentials through the
[Registry API](../../reference/agent-harness/#installation-and-api). Agent
credentials that already exist can also be replaced through Portal. A raw pull
Secret is not automatically adopted or copied into the managed source
namespace.

The managed credential covers one registry host derived from the Agent image.
It does not supply the credentials of another registry for Desktop, Terminal,
or other platform images. Those images must have their own node or platform
pull path that works. Before you retire the old access method, examine fresh
pulls, not only images that are already in use or cached. Standalone Agent
chart `imagePullSecrets` and Portal Deployment `imagePullSecrets` are separate
settings, and they are still supported.

## Deployment commands

Run these commands from the root of your clone:

| Command | Result | Connects to | Writes to |
| --- | --- | --- | --- |
| `make deploy-plan DEPLOY_CONFIG=<file>` | Validates the environment file and renders the charts | Nothing | Nothing |
| `make deploy DEPLOY_CONFIG=<file>` | Examines the target, and installs or upgrades the three releases | The cluster and the registry. With `publicTLS`, also two public DNS resolvers, and Azure in Azure DNS mode. cert-manager in the cluster connects to Let's Encrypt | The cluster. With `publicTLS`, also its `stateDirectory`, and the Public IP in Azure DNS mode |
| `make deploy-verify DEPLOY_CONFIG=<file>` | Examines the public endpoint. Only with `publicTLS` | The cluster, the public DNS resolvers, and the public hostname. Also Azure in Azure DNS mode | Nothing |
| `make publish REGISTRY=<registry>` | Builds and pushes all images and charts | The registry, and the sources of the image builds | The registry |
| `make publish-images` or `make publish-charts` | Builds and pushes only the images, or only the charts | The same as `make publish` | The registry |
| `make build-images` | Builds the images locally | The sources of the image builds | The local Docker image store |
| `make artifact-check` | Examines the release inventory | Nothing | Nothing |
| `make set-version VERSION=<version>` | Sets the version in the source files | Nothing | The source files |

For one run, `REGISTRY` and `VERSION` override `release.registry` and
`release.version`. Set them as environment variables or on the `make` command
line. They do not change the environment file. Use the same values for the plan
and the deployment. Refer to the
[platform chart README]({{< repo "app/cetirun-platform/README.md" >}}#override-release-coordinates-for-a-test-build).

## Values that make deploy sets

`make deploy` sets these Helm values from the other inputs of the environment
file. Do not set them under a `values` field. If the file sets one of them to a
different value, the run stops before it writes. The message names the value,
for example `platform.values.cetirun-operator.namespace conflicts with the
complete deployment inputs`. You can set only `global.memoryContext.image` to
another value.

Values of the Istio release, under `istio.values`:

| Values | Value that `make deploy` sets |
| --- | --- |
| `domain`, `clientId`, `tenantId` | `domain`, `identity.portalClientId`, `identity.tenantId` |
| `gateway.namespace`, `egress.namespace` | `istio.ingressNamespace`, `istio.controlPlaneNamespace` |
| `portalAuth.clusterHost` | `cetirun-portal.<platform namespace>.svc.cluster.local` |
| `portalAuth.mtls.enabled`, `portalAuth.mtls.serverIdentity` | `true`, and the SPIFFE identity of the Portal service account |

Values of the Broker release, under `broker.values`:

| Values | Value that `make deploy` sets |
| --- | --- |
| `image.repository`, `image.tag` | `<registry>/cetirun-agent-broker`, `release.version` |
| `tls.enabled`, `istio.sidecarInjection`, `istio.revision` | `true`, `true`, `istio.revision` |
| `portal.namespace`, `operator.namespace`, `toolService.namespace` | `platform.namespace` |
| `agentIdentity.portalOrigin`, `agentIdentity.portalBaseUrl`, `agentIdentity.redirectUri` | `https://<domain>`, and `https://<domain>/api/v1/connections/callback` for the redirect |
| `agentIdentity.tenantId`, `agentIdentity.blueprintClientId`, `agentIdentity.authorizationClientId`, `agentIdentity.encryptionClientId`, `agentIdentity.keyVaultKeyUrl` | The fields of `identity` with the same names |

Values of the platform release, under `platform.values`:

| Values | Value that `make deploy` sets |
| --- | --- |
| `global.registry`, `global.version` | `release.registry`, `release.version` |
| `global.portalChat.enabled`, `global.portalChat.identityRealm` | `true` while the Operator is enabled, and `identityRealm` |
| `global.memoryContext.image` | `<registry>/cetirun-memory-context:<version>` |
| `istio.enabled`, `istio.oauth2Proxy.namespace` | `true`, `istio.namespace` |
| `cetirun-operator.namespace`, `cetirun-portal.aiGateway.catalogNamespace` | `platform.namespace` |
| `cetirun-operator.domainSuffix`, `cetirun-portal.istio.host` | `domain` |
| `cetirun-operator.istioRevision` | `istio.revision` |
| `cetirun-operator.istioGateway`, `cetirun-portal.istio.gateway` | `<istio.ingressNamespace>/cetirun-gateway` |
| `cetirun-operator.cetirun.helmValues.networkPolicy.istioIngressNamespace`, `cetirun-portal.istio.gatewayNamespace` | `istio.ingressNamespace` |
| `cetirun-portal.istio.gatewayServiceAccount` | `istio.ingressServiceAccount` |
| `cetirun-operator.customRuntime.enabled`, `cetirun-operator.memoryContext.enabled`, `cetirun-operator.sandboxAdmission.enabled`, `cetirun-operator.sandboxAdmission.enforce`, `cetirun-operator.agentIdentity.enabled` | `true` |
| `cetirun-operator.connectionsMCP.url`, `cetirun-operator.connectionsMCP.brokerNamespace`, `cetirun-operator.connectionsMCP.brokerServiceAccount` | The Connections MCP Service, `broker.namespace`, and the Broker service account |
| `cetirun-operator.agentIdentity.brokerURL`, `cetirun-operator.agentIdentity.tenantId`, `cetirun-operator.agentIdentity.blueprintClientId` | The Broker Service, `identity.tenantId`, `identity.blueprintClientId` |
| `cetirun-portal.istio.internalAuthMTLS`, `cetirun-portal.features.agentToolsEnabled`, `cetirun-portal.features.agentIdentityEnabled` | `true` |
| `cetirun-portal.features.allowedAgentTypes` | `[hermes, openclaw, custom]` |
| `cetirun-portal.agentIdentity.tenantId`, `cetirun-portal.agentIdentity.portalClientId`, `cetirun-portal.agentIdentity.brokerURL`, `cetirun-portal.agentTools.brokerURL` | `identity.tenantId`, `identity.portalClientId`, and the Broker Service |
| `cetirun-portal.sandboxStorage.platformNamespace`, `cetirun-portal.sandboxStorage.platformConfigMapName` | `platform.namespace` and the platform source of the Operator, when Sandbox sources are enabled |
| `cetirun-connections-mcp.enabled`, `cetirun-connections-mcp.istio.sidecarInjection` | `true` |
| `cetirun-connections-mcp.image.repository`, `cetirun-connections-mcp.image.tag` | `<registry>/cetirun-connections-mcp`, `release.version` |
| `cetirun-connections-mcp.broker.baseURL`, `cetirun-connections-mcp.istio.brokerNamespace`, `cetirun-connections-mcp.istio.controlPlaneNamespace`, `cetirun-connections-mcp.istio.revision`, `cetirun-connections-mcp.portal.baseURL` | The Broker Service, `broker.namespace`, `istio.controlPlaneNamespace`, `istio.revision`, and `https://<domain>` |
| Under `cetirun-operator.skillMarketplace`: `enabled`, the `repository` fields `url`, `ref`, `path`, `auth`, `existingSecret`, and `syncInterval`, and the `storage` fields `resourceGroup`, `storageAccount`, `containerName`, and `storageClassName`. Under `cetirun-portal.skillMarketplace`: `enabled` and `catalogNamespace` | The top-level `skillMarketplace` block, and `platform.namespace` for the catalog. Without that block, both `enabled` values are `false` |
| Under `cetirun-observability`: `enabled`, `clusterName`, `adx.endpoint`, `adx.databases.logs`, `adx.databases.metrics`, `adx.databases.agentLogs`, and `ingestor.clientId`. With an `alerter` block: `alerter.endpoint`, `alerter.clientId`, and `alerter.destination` | The top-level `observability` block, which `make deploy` requires, and its optional `alerter` block. `enabled` is always `true`. A database that the block does not name gets its default name |

`make deploy` also writes these values without a conflict check. A value that
you set for them under `platform.values` has no effect:

- `cetirun-operator.cetirun.helmValues.skills` gets the top-level
  `sharedSkills` block, with `shared: true`. To set another runtime `skills`
  value for all Agents, for example `npmRegistry`, put it in `sharedSkills`.
- `cetirun-operator.aiGateway.masterKeySecret` gets the master key Secret of
  the AI Gateway.
- `cetirun-operator.sandboxAdmission.portalServiceAccount` gets the Portal
  service account. The Broker values `portal.serviceAccount`,
  `operator.serviceAccount`, and `toolService.serviceAccount` get the service
  accounts of the Portal, the Operator, and Connections MCP.
- `cetirun-portal.admins.aliases` gets `ownerAlias` in addition to the aliases
  that you list.

`make deploy` refuses these settings:

- `false` for `cetirun-portal.enabled`, `cetirun-ai-gateway.enabled`,
  `cetirun-ai-gateway.postgresql.bundled.enabled`, or
  `cetirun-ai-gateway.redis.bundled.enabled`
- A Broker without Microsoft delegated access
- An OAuth2 Proxy mode other than `entra-id`
- An inline `cookieSecret`, TLS certificate or key, or provider credential. In
  the Istio values, `tls.enabled: true` counts as an inline certificate
- With `publicTLS`, the Istio value `gateway.includeWildcardHost: true`.
  `make deploy` sets it to `false`
- `image`, `hermes.image`, or `openclaw.image` in
  `cetirun-operator.cetirun.helmValues`
- An `instances` list

## Install without make deploy

The component charts are for development and standalone installations. Do not
install them over the resources of a `make deploy` installation, because the
platform chart does not adopt resources that standalone Helm releases own. To
install the platform chart directly with Helm, refer to
[Use the component chart directly]({{< repo "app/cetirun-platform/README.md" >}}#use-the-component-chart-directly)
and to the [platform values](../../reference/helm/#platform-chart).

### Independent component charts

Render the operator chart locally with example values:

```bash
helm template cetirun-operator app/cetirun-operator/chart \
  --namespace cetirun-system \
  --set-string domainSuffix=example.com \
  --set-string image.repository=registry.example.com/cetirun-operator \
  --set-string image.tag=example-build \
  --set-string cetirun.chart.repoURL=oci://registry.example.com/helm/cetirun \
  --set-string cetirun.chart.version=0.1.1 \
  --set cetiadmin.enabled=false > /dev/null
```

This render runs without cluster or registry access. The operator chart
enables CetiAdmin by default. An enabled CetiAdmin needs owner, budget,
Kubernetes API, and admission inputs, so this render disables it. During
installation, examine artifact availability and rollout status. For the agent
chart, refer to [runtime render](../../reference/helm/#offline-rendering).

The operator helper sets no CetiAdmin values, so the example below disables
CetiAdmin with `HELM_SET_ARGS`. To install CetiAdmin, use a direct Helm
installation with a reviewed values file instead. Supply the inputs that
[CetiAdmin](../../operations/cetiadmin/#configure-cetiadmin) lists.

After you complete the prerequisites, run the deployment scripts from the
repository root. Before you run these commands, replace all example values:

```bash
REGISTRY=registry.example.com CHART_VERSION=0.1.1 IMAGE_TAG=example-build \
CETIRUN_CHART_VERSION=0.1.1 DOMAIN=example.com \
HELM_SET_ARGS="cetiadmin.enabled=false" \
  bash app/cetirun-operator/deploy/deploy.sh

REGISTRY=registry.example.com CHART_VERSION=0.1.1 IMAGE_TAG=example-build \
ISTIO_HOST=example.com ISTIO_REV=your-installed-istio-revision \
NOTICE_BANNER="" \
  bash app/cetirun-portal/deploy/deploy.sh
```

Replace every example value. The version `0.1.1` is an example. It does not
show that an artifact exists in your registry. `example-build` is not a
published image tag.

Use verified image digests through `IMAGE_DIGEST`. `CHART_VERSION` selects the component chart, and
`CETIRUN_CHART_VERSION` selects the runtime chart. `IMAGE_TAG`/`IMAGE_DIGEST`
pin only the component that you install, not the runtime images.

The [operator helper]({{< repo "app/cetirun-operator/deploy/deploy.sh" >}})
examines the StorageClass and shared gateway before it installs the operator.
The operator chart bundles all four CRDs as ordinary templates.
Helm upgrades the CRDs with the release, and their retention annotation keeps
them on uninstall.

The default compatible admission mode tolerates newer CR fields. It does not
disable the validation or authorization that already exists. No external CRD
apply script is necessary for compatible schema changes. Before you upgrade an
installation that already exists, review
[API compatibility](../../reference/crds/#existing-installations).

The [portal helper]({{< repo "app/cetirun-portal/deploy/deploy.sh" >}})
expects that the operator/CRDs are prepared separately. It also labels its
namespace for Istio injection. Set `ISTIO_REV` to the actual installed revision
instead of its built-in `asm-1-27`. Supply `ISTIO_HOST` explicitly, because the
helper also reads it in its final summary. Neither helper waits for the
operator or portal rollout.

`LOCAL_CHART=1` selects the local operator or portal chart. It does **not** make
the installation offline. Images and the operator-managed agent chart must
still be available.

Review [chart configuration](../../reference/helm/) for image pins, OCI pull
credentials, deployment-wide Helm values, default model, admin aliases, and
portal features. Before you serve users, configure these explicitly. The
operator helper supports simple extra `HELM_SET_ARGS`, but the portal helper
does not. For settings that a helper does not expose, use a separately reviewed
direct Helm install with a private values file. With this install, keep all
CRD, gateway, namespace-label, and authentication prerequisites.

The default namespaces and resource names are cross-component contracts. If
you change them, you must also review the shared chart's `portalAuth` service
address, gateway namespace/service-account policies, and controller routes. An
available `NAMESPACE` or `RELEASE_NAME` override does not establish that every
consumer is reconfigured automatically.

In particular, the operator helper's `NAMESPACE` does not override the chart's
`namespace` value. A non-default installation must set both consistently. The
Portal's [Upgrade action](../../guides/lifecycle/#pin-and-update-a-chart) also
finds the fixed `cetirun-system/cetirun-operator` Deployment. A change to
those Helm settings does not reconfigure this lookup.

By default, the standalone portal helper uses the namespace `cetirun-portal`.
When you examine an independent installation, use that namespace instead.
