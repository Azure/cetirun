---
title: "Use skills"
description: "Find the skills that your Agent has, read the Team skills status, and add your own skills."
weight: 55
---

A skill is a set of instructions and files that teaches your Agent a task.
To use a skill, describe the task to the Agent. The Agent finds the skill that
matches the task and reads it. You do not start a skill yourself.

A Hermes or OpenClaw Agent can have three types of skills:

| Type | Where it comes from | Can you change it? |
| --- | --- | --- |
| Platform skills | The platform installs them. | Do not change them. |
| Team skills | A Portal administrator selects them for your Team. | No. |
| Your own skills | You add them in your home directory. | Yes. |

A Custom Agent loads skills only when its image supports them.

## Platform skills

The platform gives your Agent these skills:

- `connected-services` uses the services that you connect in **Identities**,
  for example Azure, GitHub, Gmail, and Google Calendar. Refer to
  [Agent Connections and Identity](../agent-identity/).
- `cetirun-memory` reads and saves the private notes of the Agent. Refer to
  [Use memory](../memory/).

These names are reserved for the platform. Do not use them for your own
skills. The Skill Marketplace does not accept Team skills with these names.

The platform copies `connected-services` into the home directory when the
Agent starts. If you change this copy, the platform keeps your version, and
your copy does not get platform updates again. You cannot change
`cetirun-memory`.

## Team skills

If your Team selects skills, the **Overview** tab of the Agent has a
**Team skills** section. A Portal administrator selects these skills for your
Team from the Skill Marketplace. You cannot change them. The changes take
effect when the Agent starts the next time.

Each skill shows its version and a status:

| Status | Meaning |
| --- | --- |
| **Published** | The Agent has this skill. |
| **Update pending** | The skills repository has a newer version. The Agent uses the version that the portal shows until the Team releases the change. |
| **Pending** | The skill is not released yet for the current selection of the Team. The next start of the Agent waits for it. |
| **Retained** | The skill is no longer valid in the skills repository. The Agent keeps the last released version. |
| **Missing** | The skill is not released, so the Agent does not have it. |

If the section says "The Team's skills changed after this Agent started", the
Agent uses the earlier versions. To get the new versions, select
**Restart Agent**. Refer to
[Restart while retaining HOME](../lifecycle/#restart-while-retaining-home).

## Add your own skills

Put each of your skills in its own directory in the skills directory of your
agent type:

| Agent type | Skills directory |
| --- | --- |
| Hermes | `~/.hermes/skills` |
| OpenClaw | `~/.openclaw/skills` |

The Agent always reads the skills in this directory. The directory is in your
home directory, so your skills are kept when the Agent restarts.

1. Open **Terminal**.
2. Make a directory for the skill, for example
   `mkdir -p ~/.hermes/skills/release-notes` for Hermes.
3. In that directory, write a `SKILL.md` file. In the file, give the skill a
   name and a description, and then write the instructions.
4. Start a new conversation with the Agent. Then describe a task that the
   skill covers. If the Agent does not find the skill, select
   **Restart Agent**, and try again.

For the file format, refer to the skill documentation of
[OpenClaw](https://docs.openclaw.ai/tools/skills) or
[Hermes](https://hermes-agent.nousresearch.com/docs/user-guide/features/skills).

> **Warning:** A skill can tell the Agent to run commands and to use your
> connected services. Add only skills that you trust. Do not put passwords,
> keys, or tokens in a skill.

Sources: [Team skills section]({{< repo "app/cetirun-portal/web/src/components/TeamSkillsBlock.tsx" >}}),
[reserved skill names]({{< repo "app/cetirun-operator/internal/skillmarketplace/skills/skills.go" >}}),
and [Agent Harness](../../reference/agent-harness/#common-initialization-and-discovery).
