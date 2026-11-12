---
title: "Configuration and skills"
description: "How the operator builds the configuration of each Agent from deployment values, Team settings, and Agent overrides, and how skills reach the Agent."
weight: 20
---

## Configuration order

The operator builds the Helm values of an Agent in a fixed order. The order is
different for standard Agents and Sandbox Agents.

### Standard Agents

For a standard Agent, the operator builds Helm values in this order:

1. Controller defaults, for example the Secret of the Agent, identity, home
   volume, and AKS Istio namespaces.
2. Deployment-wide `cetirun.helmValues` / `CETIRUN_HELM_VALUES`.
3. Referenced `Team` member context and skills.
4. Controller-derived integration settings and image repositories.
5. Per-Agent `spec.helm.values`.
6. The enforced agent type, the OpenClaw native UI base path, and the AI
   Gateway projection.
7. The profile of the agent type resolves the image and the port. Then it
   builds the native configuration for the chart's reserved `runtime` input.

The AI Gateway projection sets the endpoint, the virtual key, the model list,
and the default model of the Agent.

### Sandbox Agents

For a Sandbox Agent, the operator builds Helm values in this order:

1. Deployment-wide `cetirun.helmValues`.
2. Team member context and skills.
3. The per-Agent overrides that a Sandbox Agent can set.
4. The AI Gateway projection.
5. The protected values of the Sandbox and the Agent Policy. The operator
   applies these values last, so they win over all other values.

The Sandbox and the Agent Policy control these values, so the operator refuses
per-Agent overrides of them:

- Workload: `replicaCount`, `serviceAccount`, `autoscaling`, and
  `sidecarScope`
- Storage and resources: `persistence` and `resources`
- Security: `securityContext`, `podSecurityContext`, `runtimeClassName`, and
  `execRestriction`
- Network: `networkPolicy` and `ingress`
- Identity: `azureWorkloadIdentity` and `agentIdentity`
- Credentials and environment: `secrets` accepts only an authorized
  `existingSecret`. `extraEnv` accepts only references to authorized Secrets
  and names that are not protected, for example not `PATH`, `HOME`, or names
  with the prefix `CETIRUN_`

### Rules for all Agents

Nested maps merge. Arrays replace and do not append. Arbitrary Helm overrides
are a privileged configuration surface. Do not expect a team default to enforce
a security policy against per-Agent overrides. The reconciler also adds the
resource version of the `gateway-token` Secret to pod annotations, so that
credential changes can roll the workload.

Select the agent type with `spec.agent.type`. The default is Hermes. Helm
values cannot select a different type. For the other fields and the supported
settings, refer to [Agent Harness](../../reference/agent-harness/).

Agent image and primary-port overrides belong in `spec.agent`, not
`spec.helm.values`. The operator also refuses overrides of the values that it
generates or protects:

- `image`: use `spec.agent.image` instead
- `runtime`, `agentRequest`, `customBindings`, and `imagePullSecrets`
- `memoryContext`, `cetiadmin`, `aiGateway`, and `connectionsMCP`
- `secrets.provider`, `secrets.authMode`, `secrets.apiBase`, `secrets.apiKey`,
  and `secrets.model`

The CRD declares each top-level `spec.helm.values` entry as an
object. Use map-shaped overrides such as `secrets`, `persistence`, or `skills`.
API admission rejects top-level scalars and arrays from a standalone values
file, but the controller can merge them internally. Refer to the
[CRD reference](../../reference/crds/).

`spec.model` selects an allowed AI Gateway model. Refer to
[Choose a model](../../guides/models/). The operator selects the default
model of an Agent in this order:

1. `spec.model`, or the Team router that `spec.model` names.
2. The operator value `aiGateway.defaultModel`.
3. The first model that the Agent can use, in the order that
   [Model access](../../operations/models/#default-model) describes.

The operator value `cetirun.defaultModel` does not decide the default model,
because the AI Gateway projection replaces `secrets.model`.

## Teams

An Agent refers to a `Team` with `spec.team`. Teams can set these items:

- Role and context rendered for the agent type.
- Up to 100 default skill identifiers. With the Skill Marketplace, the
  identifiers without the `npm:` prefix are the names of marketplace skills.
  The Sandbox Agents of a policy-bound Team do not install `npm:` packages.
- A shared Blob storage location for read-only skills.
- One Agent Policy (`agentPolicyRef`), the Sandboxes that its Agents can use
  (`sandboxRefs`), and the default Sandbox (`defaultSandboxRef`). These fields
  make the Team a policy-bound Team.
- Up to 10 smart-routing models (`modelRouters`).

The Agent Policy of a policy-bound Team decides the models, GitHub Copilot, the
budget, and the request limits of its Agents. A Team holds no provider
credentials.

A Team has no members. CetiRun does not check who selects a Team, and a Team
does not give access to the Agents of other users. For the bindings, read
[Configure a team](../../guides/teams/) and the [Glossary](../glossary/).

## Runtime files and updates

Initialization first examines tools. If image HOME was not copied before,
initialization seeds it into the PVC. The shared `.cetirun-home-seeded` flag
controls only that copy. After that, native preparation, configuration
application, integration setup, and verification run. A flag does not skip
those stages.

During pod initialization, Go-generated configuration is merged into the
persistent home. Chart-specified keys take precedence over the runtime
configuration that already exists. Runtime keys that the chart does not set can
survive. This merge does not make every file continuously synchronized:

| File | Update behavior |
| --- | --- |
| OpenClaw `.openclaw/openclaw.json` | Deep-merged from the ConfigMap at initialization |
| OpenClaw `.openclaw/workspace/USER.md` | Seeded for a new home. An initialized home keeps its own copy |
| Hermes `.hermes/config.yaml` | Deep-merged from the ConfigMap at initialization |
| Hermes `.hermes/memories/USER.md` | Seeded at the first initialization if the file does not exist |
| Hermes `.hermes/memories/TEAM.md` | Synchronized from rendered team context at every initialization. Removed when the context is empty |

JSON/YAML and environment-file updates validate inputs before they replace
files. Malformed files that already exist are kept, and initialization fails.
They are not replaced with empty defaults. Replacement is atomic for each file,
not a transaction across all files. A failed native initialization resumes and
does not copy HOME again.

OpenClaw doctor is still an optional init container after native preparation.
Configured npm skill installation follows it. Hermes continues directly from
native preparation to the optional npm step. No step does cross-Agent
migration or scans old credential stores as part of routine initialization.

A team update can roll member pods, but it does not overwrite an OpenClaw
`USER.md` that already exists. To make sure that the agent received a context
change, examine the runtime file.

Sources: [common initialization]({{< repo "app/cetirun-chart/configs/init-home-common.sh" >}}),
[OpenClaw initializer]({{< repo "app/cetirun-chart/agents/openclaw/bootstrap/init-home.sh" >}})
and [Hermes initializer]({{< repo "app/cetirun-chart/agents/hermes/bootstrap/hermes-init-home.sh" >}}).

## Skills

Skills are directories with a `SKILL.md` entry point. This table shows where
an Agent reads each kind of skill and when a change reaches the Agent.

| Skill source | Where the Agent reads it | Agents that get it |
| --- | --- | --- |
| Team skills from shared storage | OpenClaw and Hermes: read-only at `skills.mountPath` (default `/home/vibe/.aks/skills`). A change appears without a restart. Custom: in the read-only `$HOME/.agents` view, which init rebuilds when a new Pod starts. A non-default `skills.mountPath` adds a read-only alias of that view | Standard Agents, from `sharedSkills` or the Team `skillStorage` |
| Team skills from a release | A copy of the Skill Marketplace release of the Team. Init makes the copy when the Pod starts. Without the Skill Marketplace, the read-only content of the published skills catalog (`sandboxSources.skillsCatalogConfigMapName`) | Sandbox Agents |
| Platform Memory skill | `/opt/cetirun-memory/skills`, or the `$HOME/.agents` view of a Custom Agent | Agents with Memory |
| Image skill `connected-services` | `~/.openclaw/skills` or `~/.hermes/skills`. Init updates it at the next start after an image upgrade. A copy that a user changed stays, and init does not update it again | OpenClaw and Hermes. Custom images do not have it |
| npm packages | `/home/vibe/.cetirun/skills/npm/` | Standard Agents |
| Personal skills | `$HOME/.agents/skills`, a writable directory in HOME | OpenClaw and Hermes |

The configuration of each agent type decides how the agent finds these
directories and which one wins. Refer to
[Memory initialization](../../reference/agent-harness/#common-initialization-and-discovery).

If the Skill Marketplace is off and a policy-bound Team names skills, the
Sandbox Agents of the Team do not start. Enable the Skill Marketplace, or
remove the skills from the Team.

The operator computes `(team skills ∪ add) − remove`, sorts the result, and
deduplicates it. If an identifier is in both override lists, removal wins. The
Team list is limited to 100 entries, and each override list to 20 entries.
`npm:` identifiers are passed to the chart's npm installer without the prefix.
Other identifiers populate `skills.clawHub`, but these entries are not
installed.

When the [Skill Marketplace](../../operations/skill-marketplace/) is enabled,
each identifier without the `npm:` prefix is the name of a marketplace skill.
The operator does not add these names to `skills.clawHub`. An Agent cannot
add or remove them with its override lists. The operator refuses such an
Agent with `AgentConfigInvalid`.

The Skill Marketplace publishes the skills
that a policy-bound Team selects from one Git repository. When a
Sandbox Agent of the Team starts, init copies the Team's
published skills into the Pod. An OpenClaw or Hermes Agent reads this copy
read-only at `$HOME/.aks/skills`, and a Custom Agent in
`$HOME/.agents/skills`. Other Agents do not receive marketplace skills.

Only standard Agents install npm packages. A Sandbox Agent with an `npm:`
entry does not start, as the
[Skill Marketplace limits](../../operations/skill-marketplace/#limits)
describe.

Npm packages are installed under `/home/vibe/.cetirun/skills/npm/`. The
templates add those directories to OpenClaw's `skills.load.extraDirs` or
Hermes's `skills.external_dirs`. The init container runs only when `skills.npm`
is non-empty. When it runs, it prunes unrequested package directories, but an
empty list does not itself remove packages that are already on the home volume.
Similarly, an empty effective team list does not clear separately configured
Helm skill lists. This mechanism is configuration, not a skill-denial policy.

Runtime npm installation uses `skills.npmRegistry`. Its default is
`https://registry.npmjs.org/`. If your environment makes it necessary, override
the registry with an approved reachable mirror.

The OpenClaw Azure DevOps Git credential helper configures `dev.azure.com`.
`openclaw.git.azureDevOps.enabled: false` skips the setup, but it does not
delete the configuration that already exists. Configure other hosts in the
Git configuration of the Agent. Initialization keeps those entries.

Team `skillStorage` enables a static PV/PVC against a Blob container that
already exists. It does not create the Azure account or populate skills. The
shared storage defaults are NFS, `azureblob-nfs-premium`, and `100Gi`.
Sandbox Agents do not mount the container of the Team `skillStorage`. Prepare
the CSI driver, storage permissions, and network access.

For shared storage, `skills.protocol`, `skills.secretName`, and
`skills.mountOptions` become static PV settings. A Helm upgrade cannot change
them in place. Before you retire the original storage, provision the replacement
storage and make sure that it works. Refer to
[storage operations](../../operations/storage/).

Source: [Helm value builder]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}})
and [chart values]({{< repo "app/cetirun-chart/values.yaml" >}}).
Installation details: [npm initializer]({{< repo "app/cetirun-chart/configs/init-skills.sh" >}}),
[OpenClaw templates]({{< repo "app/cetirun-chart/templates/agents/openclaw/_runtime.tpl" >}}),
[Hermes templates]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}}),
[shared PV]({{< repo "app/cetirun-chart/templates/shared-skills-pv.yaml" >}}),
and [Skill Marketplace claims]({{< repo "app/cetirun-operator/internal/skillmarketplace/claims.go" >}}).
