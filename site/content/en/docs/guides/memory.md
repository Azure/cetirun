---
title: "Use memory"
description: "Find what your Agent remembers, which conversations use its memory, and how to pause, limit, correct, and delete notes."
weight: 50
---

Your Agent keeps a private memory across conversations. With this memory, the
Agent can use what earlier conversations established, and you do not have to
tell it again. Memory is on by default in Hermes and OpenClaw Agents, unless
your deployment disables it. A Custom Agent uses memory only when its image
supports it.

## What the Agent remembers

The Agent saves short notes. Each note keeps its source and its date. The Agent
saves these types of information:

- Facts that you tell it about yourself, your work, and your plans.
- Your preferences and constraints.
- Decisions.
- Its own recommendations. The Agent marks them as its own conclusions.
- The results of tasks.

The Agent does not save complete conversations, raw tool output, or secrets.
To save a preference immediately, start a message with
`Remember my preference:`.

The Agent can also learn a better method from its mistakes. To test a new
method, it can use a maximum of four extra model requests in 24 hours. These
requests use the AI Gateway key of the Agent, like its other model requests.

## Where the memory is

The memory is in `~/.cetirun/memory` in the home directory of the Agent. Only
this Agent uses it. Your other Agents and your Team cannot read it. All the
conversations of the Agent that can use memory share this one memory.

The memory stays when the Agent restarts, when you suspend and resume it, and
when you upgrade it. When you delete the Agent, the memory is deleted too.

Do not edit the files in `~/.cetirun/memory` yourself. After you edit a file
there, the Agent cannot save notes until you approve a repair. A changed or
damaged state file can make all of the memory unavailable. To change a note,
ask the Agent.

## Which conversations use memory

By default, every conversation that Hermes or OpenClaw accepts can read and
save notes:

| Conversation | Hermes | OpenClaw |
| --- | --- | --- |
| Your sessions in the Webapp, the Control UI, the CLI, or the TUI | Yes | Yes |
| Message channels, including group chats and channels | Yes | Yes |
| Scheduled jobs | Yes | Yes |
| Heartbeats | — | Yes |
| ACP sessions | Yes | Yes |
| Sub-agents that one of these conversations starts | Yes | Yes |

> **Warning:** In a group chat or a channel, a reply of the Agent can include
> facts from your private memory. All the members of the conversation can read
> the reply. The members can also add notes, and the Agent then uses these
> notes in your own conversations. The `/memory` commands do not give this
> protection. Before you connect the Agent to a shared conversation,
> [limit memory to your own conversations](#limit-memory-to-your-own-conversations).

## Pause memory in a conversation

The `/memory` commands work in OpenClaw Agents, but not in Hermes Agents.
Hermes has its own `/memory` command, which approves or rejects changes to the
[native memory files](#memory-and-the-native-memory-files) of Hermes. A Hermes
Agent answers these commands with a reply that starts with "Unknown /memory
subcommand", and memory stays on. For a Hermes Agent, use `owner_access`
instead. Refer to
[Limit memory to your own conversations](#limit-memory-to-your-own-conversations).

In an OpenClaw Agent, send one of these commands as a complete message:

| Command | Effect in this conversation |
| --- | --- |
| `/memory off` | The Agent does not read or save notes. |
| `/memory no-capture` | The Agent reads notes, but it does not save new notes. |
| `/memory on` | The Agent reads and saves notes again. |

A command applies only to the conversation where you send it. If the Agent
restarts, or if you continue the conversation later, the strictest command of
that conversation applies again. This is also true if you sent `/memory on`
after it.

The commands do not delete notes. They also do not stop the conversation
history of OpenClaw.

## Limit memory to your own conversations

To limit memory, add an `owner_access` object to the configuration of Hermes
or OpenClaw. The platform keeps this object when the Agent restarts.

| Agent type | File | Put `owner_access` under |
| --- | --- | --- |
| Hermes | `~/.hermes/config.yaml` | `plugins.entries.cetirun-memory-context.settings` |
| OpenClaw | `~/.openclaw/openclaw.json` | `plugins.entries.cetirun-memory-context.config` |

> **Warning:** An error in this file can stop the Agent from starting. Then
> you cannot open **Terminal**, because the portal enables it only for a
> **Ready** Agent. You cannot correct the file yourself, and your administrator
> must correct it.

1. Open **Terminal**.
2. Open the file for your agent type in an editor, for example `nano`.
3. Add the `owner_access` object under the key in the table. Use the keys that
   the file already has. Do not add a second key with the same name.
4. Save the file.
5. For OpenClaw, examine the file with `jq empty ~/.openclaw/openclaw.json`.
   If the command shows an error, correct the file.
6. In the portal, select **Restart Agent**.

In these examples, memory in Discord is limited to the direct messages that
you send. Replace `<your-discord-user-id>` with your Discord user ID, not your
name. Keep the quotation marks around the ID. Without them, the Agent cannot
use memory. The other conversations, for example your sessions in the Webapp
or the Control UI, do not change.

The Hermes example is in YAML and shows the keys above `owner_access`. The
file already has a `plugins` key. Add `entries` under that key. Do not add a
second `plugins` key:

```yaml
plugins:
  entries:
    cetirun-memory-context:
      settings:
        owner_access:
          senders:
            discord: ["<your-discord-user-id>"]
          chat_types:
            discord: [dm]
```

The OpenClaw example is in JSON. The `cetirun-memory-context` entry is already
in the file. Put `owner_access` in the `config` object of this entry. If the
entry has no `config` object, add one:

```json
"owner_access": {
  "senders": {"discord": ["<your-discord-user-id>"]},
  "chat_types": {"discord": ["direct"]}
}
```

You can also limit memory by platform, by conversation, and for sub-agents,
scheduled jobs, and ACP. For all the options and the names that Hermes and
OpenClaw use, refer to
[Memory access for OpenClaw and Hermes]({{< repo "app/cetirun-memory-context/OWNER_ACCESS.md" >}}).

## See, correct, and delete notes

The Agent manages its notes for you. Ask it in a conversation:

| Task | What to ask |
| --- | --- |
| See what it remembers about a topic | "What do you remember about my travel plans?" |
| Correct a note | "That is wrong. My manager is now Kim." The Agent updates the note and keeps its sources. |
| Delete a note | "Forget my old address." The Agent shows a plan of what it deletes. After you confirm, it deletes the note. |
| Add notes from a file | "Import my notes from `~/notes/projects.md`." The Agent plans the import of the files that you select. It refuses files that usually hold credentials. |

When the Agent forgets a note, it does not delete the conversation history of
Hermes or OpenClaw, the logs of the platform, or backups.

## Memory and the native memory files

Hermes also has its own memory files, for example
`~/.hermes/memories/MEMORY.md` and `USER.md`. These files are separate from
the CetiRun memory. The `/memory` command of Hermes applies only to these
files. The Agent does not copy them into the CetiRun memory automatically. To
add their content, ask the Agent to import them.

For administrators, [Agent Harness](../../reference/agent-harness/#private-memory-context)
describes the memory component.

Sources: [memory skill]({{< repo "app/cetirun-memory-context/assets/skills/cetirun-memory/SKILL.md" >}}),
[memory access]({{< repo "app/cetirun-memory-context/OWNER_ACCESS.md" >}}),
and [Hermes configuration writer]({{< repo "app/cetirun-chart/agents/hermes/bootstrap/hermes-apply-config.py" >}}).
