---
title: "Portal administration"
description: "Find the pages of the Portal Admin area, the resources that each page changes, and the settings that only kubectl can change."
weight: 12
---

The **Admin** area of the Portal is for Portal administrators. Its pages
create and change Teams, Agent Policies, and Sandboxes. They also show all
Agents of the installation. This page lists the Admin pages and their limits.

## Portal administrators

A user is a Portal administrator when the alias of the user is in
`cetirun-portal.admins.aliases`. `make deploy` adds the `ownerAlias` of the
environment file to this list. To add other Portal administrators, list their
aliases under `platform.values.cetirun-portal.admins.aliases`. For how the
Portal identifies users, refer to [Security](../../security/).

Two other administrator roles are separate:

- **Admission administrators** write policy-bound configuration with
  `kubectl`, and they reauthorize Agents. The operator values
  `sandboxAdmission.adminUsers` and `sandboxAdmission.adminGroups` name them.
  Refer to [the admission certificate](../policy-teams/#the-admission-certificate).
- **Platform administrators** sign in to the console of the AI Gateway with its
  console password. Refer to [Open the console](../ai-gateway/#open-the-console).

The Portal writes the changes of the Admin pages with its own ServiceAccount.
The Sandbox admission webhook accepts this ServiceAccount as an admission
administrator. As a result, a Portal administrator does not have to be an
admission administrator to use the Admin pages.

## Admin pages

| Page | Path | What it changes | Resource |
| --- | --- | --- | --- |
| **Teams** | `/admin/teams` | The Agent Policy, the allowed Sandboxes, and the default Sandbox of each Team. Also the member context, the skills, and the smart-routing models. **Details** shows the compatibility report of the Team | `Team` |
| **Agent policies** | `/admin/policies` | The harnesses, skills, shared models, GitHub Copilot, budget, request limits, identity, Sandbox selection, security, network, and ingress mode of each Policy | `AgentPolicy` |
| **Sandboxes** | `/admin/sandboxes` | The runtime, the resources of the Agent and its helpers, the HOME StorageClass and size, and the scratch size | `Sandbox` |
| **Skills** | `/admin/skills` | Nothing. It shows the state of the Skill Marketplace. It appears only when the Skill Marketplace is enabled | None |
| **Agents** | `/admin/instances` | The display name, image, Web UI, and model of each Agent. It also deletes Agents, and it shows their status and events. Its page title is **All agents** | `Agent` |

The navigation also shows **Settings**. This entry is not available.

For the tasks that use these pages, refer to:

- [Policy-bound Teams](../policy-teams/#check-the-setup) for the first
  Sandbox, Policy, and Team
- [Configure a team](../../guides/teams/) for the settings of a Team
- [Smart routing](../smart-routing/) for routers
- [Skill Marketplace](../skill-marketplace/#select-the-skills-of-a-team) for
  Team skills
- [Manage lifecycle](../../guides/lifecycle/) for the Agents of other users

## Settings that only kubectl can change

The Policy editor shows these settings, but you cannot change them in the
Portal. When you save other settings, the Portal keeps them:

| Setting in the editor | Field of the Agent Policy |
| --- | --- |
| Kubernetes API access | `spec.kubernetesAccess` |
| Blocked FQDNs | `spec.network.blockedFQDNs` |
| Ingress mTLS required | `spec.network.ingress.requireMTLS` |

To change them, apply the Agent Policy with `kubectl` as an admission
administrator. The registration of the platform must also support each
setting. Refer to
[Policy settings and registration capabilities](../policy-teams/#policy-settings-and-registration-capabilities).

## Save changes

The Portal saves a resource only if nobody changed it after you opened it. If
the save reports a version conflict, reload the editor. Then apply your change
again.

For a Team, only a change to its configuration counts, not a status update. If
the configuration of the Team changes while you edit it, the editor keeps your
draft and asks you to reload. The reload discards the draft. The deletion
dialog of a Team shows its errors in the dialog. After a conflict, reload the
Team and examine it before you delete it.

When the cluster refuses a change, the editor shows the known field paths and
the validation reasons. Correct these fields before you save again. For other
errors, the Portal shows a general message. Examine the Portal logs for
details. For an HTTP 403 from the Sandbox admission webhook, refer to
[Team operations rejected by admission](../policy-teams/#team-operations-rejected-by-admission).

Sources: [Admin routes]({{< repo "app/cetirun-portal/web/src/App.tsx" >}}),
[Admin navigation]({{< repo "app/cetirun-portal/web/src/pages/admin/AdminLayout.tsx" >}}),
[Policy editor]({{< repo "app/cetirun-portal/web/src/pages/admin/PolicyEditPage.tsx" >}}),
and [Portal values]({{< repo "app/cetirun-portal/chart/values.yaml" >}}).
