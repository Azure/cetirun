---
title: "Configuration and skills"
description: "Understand team defaults, instance overrides, model selection, and shared skills."
weight: 20
---

## Configuration order

The operator builds Helm values in this order:

1. Controller defaults, for example, the instance Secret, identity, home
   volume, and AKS Istio namespaces.
2. Deployment-wide `cetirun.helmValues` / `CETIRUN_HELM_VALUES`.
3. Referenced `Team` member context and skills.
4. Controller-derived integration settings and image repositories.
5. Per-instance `spec.helm.values`.
6. Enforced Agent type, OpenClaw native UI base path and Copilot model
   selection.
7. The selected Harness profile resolves the image and port. Then it builds the
   native configuration for the chart's reserved `runtime` input.

Nested maps merge. Arrays replace and do not append. Arbitrary Helm overrides
are a privileged configuration surface. Do not expect a team default to enforce
a security policy against instance overrides. The reconciler also adds the
gateway Secret's resource version to pod annotations, so that credential
changes can roll the workload.

Select the Harness through `spec.agent.type`. The default is Hermes. Helm values
cannot select a different type. For the other fields and supported runtime
settings, refer to [Agent Harness](../../reference/agent-harness/).

Agent image and primary-port overrides belong in `spec.agent`, not
`spec.helm.values`. The controller also rejects `runtime` and `agentRequest`
overrides, because these are generated platform inputs.

The CRD declares each top-level `spec.helm.values` entry as an
object. Use map-shaped overrides such as `secrets`, `persistence`, or `skills`.
API admission rejects top-level scalars and arrays from a standalone values
file, but the controller can merge them internally. Refer to the
[CRD reference](../../reference/crds/).

`spec.model` selects an allowed AI Gateway model. Refer to
[model selection](../../guides/models/).

## Teams

An instance refers to a `Team` with `spec.team`. Teams can set these items:

- Role and context rendered for the selected runtime.
- Up to 100 default skill identifiers. With the Skill Marketplace, these are
  the names of marketplace skills and `npm:` packages.
- A shared Blob storage location for read-only skills.

A team reference is not a check of organizational membership, and it does not
grant shared access to another user's instance.

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

Skills are directories with a `SKILL.md` entry point. User workspace skills are
on the persistent home. Runtime init composes team-shared material and the
platform Memory skill into a read-only `$HOME/.agents/skills` view. This view is
refreshed for each new Pod, not by changes to the shared source during the
lifetime of a Pod. Runtime-specific configuration controls how the agent
discovers and prioritizes these directories. Refer to
[Memory initialization](../../reference/agent-harness/#common-initialization-and-discovery).

The operator computes `(team skills ∪ add) − remove`, sorts the result, and
deduplicates it. If an identifier is in both override lists, removal wins. The
Team list is limited to 100 entries, and each override list to 20 entries.
`npm:` identifiers are passed to the chart's npm installer without the prefix.
Other identifiers populate `skills.clawHub`, but these entries are not
installed.

When the [Skill Marketplace](../../operations/skill-marketplace/) is enabled,
each identifier without the `npm:` prefix is the name of a marketplace skill.
The operator does not add these names to `skills.clawHub`. An instance cannot
add or remove them with its override lists. The operator refuses such an
instance with `AgentConfigInvalid`.

The Skill Marketplace publishes the skills
that a policy-bound Team selects from one Git repository. When a
common-executor Sandbox Agent of the Team starts, init copies the Team's
published skills into the Pod. An OpenClaw or Hermes Agent reads this copy
read-only at `$HOME/.aks/skills`, and a Custom Agent in
`$HOME/.agents/skills`. Other Agents do not receive marketplace skills.

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
instance's Git configuration. Initialization keeps those entries.

Team `skillStorage` enables a static PV/PVC against a Blob container that
already exists. It does not create the Azure account or populate skills. The
shared storage defaults are NFS, `azureblob-nfs-premium`, and `100Gi`. The
common discovery path is `$HOME/.agents/skills`. Native
`skillStorage.mountPath` can add a read-only alias. Prepare the CSI driver,
storage permissions, and network access.

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
