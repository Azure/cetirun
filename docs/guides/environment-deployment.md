# Deploying `main` to an Environment

See the canonical [Infrastructure operations](../../site/content/en/docs/operations/infrastructure.md)
documentation for the broader cluster provisioning and deployment context.

The `Environment Deploy` workflow publishes immutable artifacts from the
latest `main` revision and upgrades an already-bootstrapped CetiRun
environment. Its initial rollout is manual-only. Manual runs always check out
`main`; they never deploy the branch from which the workflow was opened. Add a
`push` trigger for `main` only in a separately reviewed change after this exact
path has completed live deployment and product acceptance.

This workflow is continuous deployment, not infrastructure provisioning. It
does not create or grant Microsoft Entra applications, Agent Identity
Blueprints, managed identities, Key Vaults, Redis, AKS, ACR, DNS, TLS, or the
operator's least-privilege ACR pull credential.

## One-time bootstrap

Before enabling the workflow for an environment:

1. Enable the AKS OIDC issuer and Workload Identity.
2. Provision the deployment Agent Identity resources with
   `scripts/agent_id_pilot.py bootstrap-client` followed by
   `provision-deployment`. Keep its state file outside the repository with mode
   `0600`.
3. Configure the Portal OAuth application to emit the `login_hint` ID-token
   claim.
4. Deploy an authenticated Redis session store for OAuth2 Proxy and restrict it
   to OAuth2 Proxy with a NetworkPolicy.
5. Create the operator's least-privilege OCI chart pull credential in the
   Operator namespace. The separate registry-credential namespace stores
   per-Agent registry credentials and cannot be mounted by the Operator Pod.
6. Adopt or upgrade the exact CetiRun CRDs through a reviewed one-time
   migration, then verify all existing Agents remain Ready and Agent
   Identity-disabled.
7. Run the manual `Environment Deploy` workflow from a reviewed `main`
   revision and complete live Agent Identity acceptance before enabling an
   automatic `main` trigger.

The deployment workflow does not migrate existing Agent chart pins. Before the
first run, explicitly upgrade or recreate any test Agent that still uses a
profile-less runtime chart, then verify that it is Ready. This keeps routine
deployment rollback limited to Helm releases instead of mutating Agent specs by
name.

Do not put the provisioning state file, tokens, kubeconfig, certificates,
authorization records, or rendered private values in GitHub Actions secrets or
artifacts.

## GitHub environment

Create a protected GitHub Environment named `cetiruntest`. Require reviewers
for deployment where the repository's operating policy calls for it. Configure
an Azure workload identity federated credential that trusts only this
repository and environment, and grant it only the permissions needed to push to
the target ACR and obtain deployment credentials for the target AKS cluster.

When renaming or replacing an existing GitHub Environment, create the new
Environment, copy its protected inputs, and add the new exact federated subject
before changing the workflow. Retain the old Environment and federated
credential until the new manual path passes acceptance; remove them only in a
separately reviewed cleanup.

Add the following environment secrets. Identifiers are kept in secrets to keep
environment-private configuration out of source and logs, even where a value is
not an authentication credential.

| Secret | Purpose |
| --- | --- |
| `AZURE_CLIENT_ID` | GitHub Actions deployment identity |
| `AZURE_TENANT_ID` | Target Entra tenant and Broker tenant |
| `AZURE_SUBSCRIPTION_ID` | Target AKS and ACR subscription |
| `AKS_RESOURCE_GROUP` | Target AKS resource group |
| `AKS_CLUSTER_NAME` | Exact AKS cluster name |
| `ACR_NAME` | Azure resource name of the target ACR |
| `ACR_REGISTRY` | Login server of that same ACR |
| `CETIRUN_DOMAIN` | Portal hostname, without `https://` |
| `DESKTOP_IMAGE_TAG` | Separately accepted Desktop sidecar image tag |
| `DESKTOP_IMAGE_DIGEST` | Digest paired with that Desktop tag |
| `HERMES_IMAGE_TAG` | Separately accepted Hermes runtime profile image tag |
| `HERMES_IMAGE_DIGEST` | Digest paired with that Hermes tag |
| `PORTAL_OIDC_CLIENT_ID` | Existing Portal OAuth client ID |
| `PORTAL_ADMIN_ALIASES` | Comma-separated Portal administrator aliases |
| `AGENT_ID_BLUEPRINT_CLIENT_ID` | Provisioned deployment Blueprint client ID |
| `AGENT_ID_AUTHORIZATION_CLIENT_ID` | Provisioned authorization client ID |
| `AGENT_ID_ENCRYPTION_CLIENT_ID` | Broker encryption UAMI client ID |
| `AGENT_ID_KEY_VAULT_KEY_URL` | Versioned Broker cache-encryption key URL |
| `AGENT_PERSISTENCE_STORAGE_CLASS` | Accepted HOME PVC storage class for this environment |
| `AGENT_PERSISTENCE_ACCESS_MODE` | Accepted HOME PVC access mode |
| `AGENT_PERSISTENCE_SIZE` | Accepted HOME PVC requested size |
| `REDIS_CONNECTION_URL` | In-cluster authenticated Redis URL |
| `REDIS_SECRET_NAME` | Existing OAuth2 Proxy Redis credential Secret |
| `ACR_DOCKER_SECRET_NAME` | Existing operator OCI chart pull Secret |
| `OPENCLAW_IMAGE_TAG` | Previously accepted OpenClaw image tag |
| `OPENCLAW_IMAGE_DIGEST` | Digest paired with that OpenClaw tag |
| `PORTAL_IMAGE_DIGEST` | Previously accepted Portal image digest |

The workflow deliberately preserves the accepted OpenClaw and Portal images
because some enterprise npm mirrors do not contain every locked dependency.
It also preserves accepted Hermes and Desktop deployment pins: built-in Agent
specs receive a stable runtime-profile image, so routine platform deployment
must not silently replace that profile. The workflow still publishes current
candidate Base, Hermes, Desktop, Operator, Terminal, AI Gateway, and Agent
Identity Broker images from `main`, but publishing a candidate does not accept
it for existing Agents. Change a runtime pin only through a separately reviewed
profile-compatibility migration. The deployment summary identifies every
preserved artifact and must not describe it as deployed from the current source.
The persistence inputs similarly preserve the environment's accepted HOME PVC
contract so a routine chart upgrade cannot silently request a different access
mode, size, or storage class for an existing StatefulSet.

## Deployment and rollback behavior

Each run creates a unique SemVer-compatible version containing the workflow run
number, run attempt, and source SHA. Validation exports the exact full SHA and
the deployment job checks out that SHA, so a newer `main` push cannot change the
source between jobs. Newly built images are resolved to digests before
deployment, and charts are installed by their exact OCI version. The deploy
helper:

1. obtains an isolated admin kubeconfig without changing the global context;
2. fails if the one-time Redis and ACR credential prerequisites are absent;
3. upgrades the Istio authentication release to Redis-backed sessions,
   reconciles the managed Istio extension provider so its verified ID token
   reaches only the Portal handoff filter, and applies the protected Portal
   administrator alias list to both authentication and Portal values;
   during first enablement it also replaces historical webhook path syntax with
   the chart's fixed Teams and apex callback allowlists;
4. upgrades the separate Agent Identity Broker release;
   it requires the Broker workload-identity label, ready Istio sidecar (including
   the native restartable-init form), STRICT peer authentication, and its
   NetworkPolicy before continuing;
5. upgrades the platform release and enables the Operator and Portal feature
   flags; and
6. verifies the Broker and platform rollouts and live Helm values; and
7. verifies that the pre-existing Agent set, UIDs, chart versions, Agent
   Identity settings, runtime selections, and Ready conditions are preserved.
   For Hermes and OpenClaw only, an omitted image or port may receive the exact
   environment default in one Operator update. Every previously populated
   runtime field remains immutable during deployment, and Custom Agents receive
   no default-pinning exception.

Every Helm upgrade is atomic. The helper records the prior Istio, Broker, and
platform revisions and, if a later stage or final verification fails, restores
the successful earlier stages in reverse dependency order. A Broker installed
for the first time is uninstalled on rollback; its namespace and external
Entra or Azure resources are retained.

The workflow uses `cancel-in-progress: false`, so a deployment is never
cancelled halfway through by a newer push. Helm release history is the immediate
Kubernetes rollback point. Entra and Azure resources are intentionally outside
Helm rollback and must be managed with their exact private provisioning state.

## Required acceptance

After the first deployment and after security-sensitive changes, verify:

- existing Agents remain Ready and Agent Identity-disabled;
- a dedicated Hermes Agent can be created with Agent Identity enabled;
- its owner can connect through the Portal;
- `cetirun-agent auth status` reports the connected owner;
- bounded Azure read-only subscription and resource-group operations succeed;
- the short-lived credential is not present in model context, environment
  variables, files, command arguments, or logs; and
- Broker restart recovery succeeds.

Automatic deployment success is not a substitute for this product-level
acceptance.
