---
title: "Use skills"
description: "Find the skills that your Agent has, read the Team skills status, and add your own skills."
weight: 55
---

A skill is a set of instructions and files that teaches your Agent a task.
To use a skill, describe the task to the Agent. You can name the skill and ask
the Agent to read its instructions first. The runtime makes eligible skills
available to the model; the model decides which instructions to read and
which tools to call. A published or discovered skill does not prove that the
model read it or ran its scripts successfully.

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

The runtime discovers eligible skills in this directory. Discovery makes a
skill available; it does not run the skill. The directory is in your home
directory, so your skills are kept when the Agent restarts.

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

## Check a bundled script invocation

Keep the directory structure of the skill intact. For example, a skill can
contain `SKILL.md` and `scripts/probe.sh`. After reading `SKILL.md`, the Agent
must use the complete script path. From the skill directory, the command is
`/bin/sh scripts/probe.sh`; `/bin/sh probe.sh` names a different file. An
absolute path to the same script also works. Changing to the skill directory
does not remove the `scripts/` component.

The CetiRun OpenClaw image includes this complete-path rule in each run's
system instructions, including runs that reuse a saved skill catalog. It
also tells the model to read the selected `SKILL.md` before using its files.
This guidance does not rewrite commands, change tool permissions, or replace
checking the execution result.

For explicit OpenClaw selections, such as `$release-notes` or
`/skill release-notes`, CetiRun also loads the complete selected `SKILL.md`
through the authorized native `read` tool after run admission and before
the model runs. A run-admission veto prevents both the read and its
activation record. The
instructions are runtime context; they do not appear as a model-issued
`read` call. Runtime activation records distinguish a complete read from
verification that the instructions remain in the context passed to the
model transport. Neither record proves that a script or model request
succeeded.

This loading applies to local skills in the current catalog with an
available native `read` tool. A denied or redirected read, missing file,
oversized document, or loss of the complete instructions after compaction
stops the selected-skill request. Remote and hidden skills, and tool modes
without the concrete reader, are not supported by this loading path.
Mentioning a skill in ordinary prose still relies on the model to read its
instructions; use an explicit selection when you need runtime loading.

If the tools finish but the model produces no final answer, a selected-skill
run uses the existing no-model fallback. The isolated tool-free finalizer
cannot re-establish authorized skill loading, so it does not make another
model request. The fallback reports that no final summary was produced;
it does not repeat completed actions.

If a skill fails, inspect the tool call and its result before repeating the
task. Check whether the Agent read the intended `SKILL.md`, used the complete
path, and received successful script output. A matching answer alone does
not establish that a tool ran. Keep the failed result when testing a changed
skill or runtime.

> **Warning:** A skill can tell the Agent to run commands and to use your
> connected services. Add only skills that you trust. Do not put passwords,
> keys, or tokens in a skill.

Sources: [Team skills section]({{< repo "app/cetirun-portal/web/src/components/TeamSkillsBlock.tsx" >}}),
[reserved skill names]({{< repo "app/cetirun-operator/internal/skillmarketplace/skills/skills.go" >}}),
and [Agent Harness](../../reference/agent-harness/#common-initialization-and-discovery).
