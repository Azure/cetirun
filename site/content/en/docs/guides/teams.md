---
title: "Configure a team"
description: "Apply shared context and provider defaults with Team."
weight: 50
---

A `Team` is a CetiRun configuration resource, not a Microsoft Teams bot.
Both the team and its member instances are cluster-scoped.
It does not define a membership roster, verify Entra group membership, or
authorize access to another user's instance. Portal administrators can manage
teams; instance owners can select a team.

Save a configuration such as:

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Team
metadata:
  name: platform
spec:
  memberContext:
    role: Engineer
    description:
      - Review changes before applying them.
  defaults:
    provider: github_copilot
```

An instance references the team with `spec.team: platform`. The team must exist:
a missing reference produces `TeamNotFound` on the instance. Team resource
changes enqueue referencing instances and change their configuration hash,
which can trigger Helm upgrades and pod rollouts.

Hermes refreshes `.hermes/memories/TEAM.md` at initialization. OpenClaw renders
the context into its initial `USER.md`, but preserves that file after home
initialization. Changing a team does not continuously rewrite every agent's
user memory. See [runtime file updates](../../concepts/configuration/#runtime-files-and-updates).

Add `spec.skills` for approved identifiers and `spec.skillStorage` for a
reviewed shared Blob container. See [configuration and skills](../../concepts/configuration/)
for installation limits and [CRD reference](../../reference/crds/) for the field
names. Install and verify each skill's external dependencies separately.
The shared storage account/container and CSI prerequisites must already exist;
the team resource does not provision cloud storage.

The team supplies a provider, not a model ID or API credential. Instance Helm
provider settings take precedence, and OpenAI/Anthropic need an explicit model
and separately prepared credentials. Effective skill IDs are
`(team skills ∪ add) − remove`; removal wins, but does not enforce a prohibition
against separate Helm configuration or user-installed files.

Applying a team or editing its provider/context triggers reconciliation of
member instances. Review the affected users before applying a change.
Deleting a referenced team is not a cascade-delete or safe detach: the portal's
delete handler does not check for members, and their next reconciliation can
fail with `TeamNotFound`. Move references deliberately before deleting a team.

Team status fields are defined in the schema but are not populated by a
team-status reconciler. Inspect each member instance's conditions and effective
configuration instead of waiting for a team-wide Ready condition.

Sources: [team event handling]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}),
[team defaults and skills]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}),
[admin team handlers]({{< repo "app/cetirun-portal/internal/handler/admin_team.go" >}}),
and [runtime initialization]({{< repo "app/cetirun-chart/agents/hermes/bootstrap/hermes-init-home.sh" >}}).
