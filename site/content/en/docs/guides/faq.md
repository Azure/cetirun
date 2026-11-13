---
title: "Frequently asked questions"
description: "Short answers to common questions about Agents, files, models, privacy, and limits, with links to the task pages."
weight: 90
---

## Can I have more than one Agent?

By default, no. Refer to [Default limits](../troubleshooting/#default-limits).

## Can I change the name, the type, or the Team of my Agent?

Not in the portal. Refer to
[Decide before you create](../../getting-started/first-instance/#decide-before-you-create).

## Where are my files?

In the home directory, `/home/vibe` for Hermes and OpenClaw. Refer to
[Where your data is](../data/#where-your-data-is).

## What do I lose when I suspend my Agent?

The GitHub Copilot sign-in and the files outside the home directory. Refer to
[What each action keeps](../data/#what-each-action-keeps).

## Does my Agent continue to run when I close the browser?

Yes, until you suspend or delete it. Refer to
[Suspend and resume your Agent](../lifecycle/#suspend-and-resume-your-agent).

## Why does my Agent report a billing or credits error?

It used its monthly model budget. Refer to
[The Agent reports a billing or credits error](../troubleshooting/#the-agent-reports-a-billing-or-credits-error).

## Can I use my own model API key?

No. Your Agent gets its models from the platform's AI Gateway. Refer to
[Choose a model](../models/).

## Can my administrators see my conversations?

Yes. Refer to [Who can see your data](../data/#who-can-see-your-data).

## Can I install software?

Yes, but only software in your home directory stays after a restart. Refer to
[Install software that stays](../data/#install-software-that-stays).

## How do I copy files into or out of my Agent?

Use Git or another tool in **Terminal**. Refer to
[Keep a copy of your files](../data/#keep-a-copy-of-your-files).

## How do I stop the Agent from remembering something?

Ask the Agent to forget the note. In an OpenClaw Agent, you can also send
`/memory off` to pause memory in one conversation. This command does not work
in a Hermes Agent. Refer to [Use memory](../memory/).

## Why can I not find Identities, Chat, or Tailscale?

Each one shows only when some conditions are true:

| Item | Conditions |
| --- | --- |
| **Identities** | Your deployment allows connected services. Custom Agents and the Agents of a policy-bound Team have more conditions. Refer to [Agent Connections and Identity](../agent-identity/). |
| **Chat** | The Agent is a Custom Agent with an HTTP port and without a Web UI, and your deployment allows Portal Chat. Refer to [Chat with a Custom Agent](../custom-chat/). |
| **Tailscale** | The Agent is a Hermes or OpenClaw Agent that is not in a policy-bound Team, and your deployment allows Tailscale. Refer to [Connect Tailscale](../tailscale/#before-you-begin). |

If the conditions are true and you still cannot find the item, ask your
platform administrator.
