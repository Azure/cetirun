---
title: "Configuration and skills"
description: "Understand team defaults, instance overrides, model selection, and shared skills."
weight: 20
---

## Configuration order

The operator builds Helm values in this order:

1. Controller defaults, including the instance Secret, identity, home volume,
   and AKS Istio namespaces.
2. Deployment-wide `cetirun.helmValues` / `CETIRUN_HELM_VALUES`.
3. Referenced `Team` member context and skills.
4. Controller-derived integration settings and image repositories.
5. Per-instance `spec.helm.values`.
6. Enforced Agent type, OpenClaw native UI base path and Copilot model selection.
7. The selected Harness profile resolves image and port, then builds native
   configuration for the chart's reserved `runtime` input.

Nested maps merge; arrays replace rather than append. Arbitrary Helm overrides
are a privileged configuration surface. Do not assume a team default enforces
a security policy against instance overrides. The reconciler also adds the
gateway Secret's resource version to pod annotations so credential changes can
roll the workload.

Select the Harness through `spec.agent.type`, defaulting to Hermes. Helm values
cannot select a different type. See [Agent Harness](../../reference/agent-harness/)
for the other fields and supported runtime settings.

Agent image and primary-port overrides belong in `spec.agent`, not
`spec.helm.values`. The controller also rejects `runtime` and `agentRequest`
overrides; these are generated platform inputs.

The CRD declares each top-level `spec.helm.values` entry as an
object. Use map-shaped overrides such as `secrets`, `persistence`, or `skills`.
Top-level scalars and arrays from a standalone values file are rejected by API
admission, even though the controller can merge them internally. See the
[CRD reference](../../reference/crds/).

`spec.model` selects an authorized AI Gateway model. The required Team's Policy
controls the available model sources; provider credentials stay at the Gateway. See
[model selection](../../guides/models/).

## Teams

An instance references a `Team` by `spec.team`. Teams can set:

- Required Policy and allowed/default Sandbox references.
- Role and context rendered for the selected runtime.
- Up to 20 default skill identifiers.
- A shared Blob storage location for read-only skills.

The team resource is distinct from **Microsoft Teams**, which is an optional
messaging integration. A team reference does not verify organizational
membership or grant shared access to another user's instance.

## Runtime files and updates

Initialization first checks tools and seeds image HOME into the PVC if it has
not been copied before. The shared `.cetirun-home-seeded` flag controls only
that copy. Native preparation, configuration application, integration setup and
verification run afterward; a flag does not skip those stages.

Go-generated configuration is merged into the persistent home during pod
initialization, with chart-specified keys taking precedence over existing
runtime configuration. Unspecified runtime keys can survive. This does not make
every file continuously synchronized:

| File | Update behavior |
| --- | --- |
| OpenClaw `.openclaw/openclaw.json` | Deep-merged from the ConfigMap at initialization |
| OpenClaw `.openclaw/workspace/USER.md` | Managed Team block refreshed at initialization; personal content outside the block preserved |
| Hermes `.hermes/config.yaml` | Deep-merged from the ConfigMap at initialization |
| Hermes `.hermes/memories/USER.md` | Seeded if missing on first initialization |
| Hermes `.hermes/memories/TEAM.md` | Synchronized from rendered team context at every initialization; removed when context is empty |

JSON/YAML and environment-file updates validate inputs before replacing files.
Malformed existing files are preserved and initialization fails; they are not
replaced with empty defaults. Replacement is atomic per file, not a transaction
across all files. A failed native initialization resumes without recopying HOME.

OpenClaw doctor remains an optional init container after native preparation.
Configured npm skill installation follows it; Hermes proceeds directly from
native preparation to the optional npm step. No step performs cross-Agent
migration or scans old credential stores as part of routine initialization.

A Team change stops and restarts the runtime to apply the new managed context
and skills. Personal content outside managed context sections and native
session history stay on the retained HOME.

Sources: [common initialization]({{< repo "app/cetirun-chart/configs/init-home-common.sh" >}}),
[OpenClaw initializer]({{< repo "app/cetirun-chart/agents/openclaw/bootstrap/init-home.sh" >}})
and [Hermes initializer]({{< repo "app/cetirun-chart/agents/hermes/bootstrap/hermes-init-home.sh" >}}).

## Skills

Skills are directories with a `SKILL.md` entry point. User workspace skills live
on the persistent home. Runtime init composes team-shared material and the
platform Memory skill into a read-only `$HOME/.agents/skills` view. It is refreshed
for each new Pod, not by changes to the shared source during a Pod's lifetime.
Runtime-specific configuration determines how the agent discovers and prioritizes
these directories. See [Memory initialization](../../reference/agent-harness/#common-initialization-and-discovery).

The operator computes `(team skills ∪ add) − remove`, sorts the result, and
deduplicates it. Removal wins when an identifier appears in both override
lists. Each list is limited to 20 entries. `npm:` identifiers are passed to the
chart's npm installer with the prefix removed. Other identifiers populate
`skills.clawHub`; these entries are not installed.

Npm packages are installed under `/home/vibe/.cetirun/skills/npm/`; the templates
add those directories to OpenClaw's `skills.load.extraDirs` or Hermes's
`skills.external_dirs`. The init container runs only when `skills.npm` is
non-empty. It prunes unrequested package directories when it runs, but an empty
list does not itself clean up packages already on the home volume. Similarly,
an empty effective team list does not clear separately configured Helm skill
lists. This mechanism is configuration, not a skill-denial policy.

Runtime npm installation uses `skills.npmRegistry`, which defaults to
`https://registry.npmjs.org/`. Override it with an approved reachable mirror
when required by your environment.

The OpenClaw Azure DevOps Git credential helper configures `dev.azure.com`.
`openclaw.git.azureDevOps.enabled: false` skips setup without deleting existing
configuration. Configure additional hosts in the instance's Git configuration;
initialization preserves those entries.

Team `skillStorage` enables a static PV/PVC against an existing Blob container;
it does not create the Azure account or populate skills. Shared storage defaults
are NFS, `azureblob-nfs-premium` and `100Gi`. The common discovery path is
`$HOME/.agents/skills`; native `skillStorage.mountPath` can add a read-only alias.
Arrange the CSI driver, storage permissions, and network access.

For shared storage, `skills.protocol`, `skills.secretName`, and
`skills.mountOptions` become static PV settings. They cannot be changed in place
by a Helm upgrade. Provision and verify replacement storage before retiring
the original. See [storage operations](../../operations/storage/).

Source: [Helm value builder]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}})
and [chart values]({{< repo "app/cetirun-chart/values.yaml" >}}).
Installation details: [npm initializer]({{< repo "app/cetirun-chart/configs/init-skills.sh" >}}),
[OpenClaw templates]({{< repo "app/cetirun-chart/templates/agents/openclaw/_runtime.tpl" >}}),
[Hermes templates]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}}),
and [shared PV]({{< repo "app/cetirun-chart/templates/shared-skills-pv.yaml" >}}).
