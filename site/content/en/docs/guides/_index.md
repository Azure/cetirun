---
title: "Using agents"
description: "Choose a model, connect accounts, chat with a Custom Agent, and manage the lifecycle of an Agent."
weight: 40
no_list: true
---

These guides are for the users of a CetiRun deployment that already exists.
You need the Portal address from your platform administrator. A few steps
change Kubernetes, Azure, or your tailnet. These steps say so, and you need the
matching permissions for them.

- [Choose a model](models/) — select shared models or GitHub Copilot through
  the AI Gateway
- [Agent Connections and Identity](agent-identity/) — give an Agent access to
  Azure, GitHub, or Google without giving it your credentials
- [Shared GitHub App](github-app/) — give an Agent access to selected
  repositories through a shared GitHub App
- [Chat with a Custom Agent](custom-chat/) — chat with a Custom Agent that has
  no Web UI, and keep the history in your browser
- [Manage your Agent](lifecycle/) — edit, restart, suspend, resume, upgrade,
  and delete your Agent
- [Tour the portal](portal/) — the header, the Agent list, the sidebar, and the
  Overview and Status tabs
- [Choose an Agent type](agent-types/) — Hermes, OpenClaw, and Custom
- [Use memory](memory/) — what the Agent remembers, and how to pause, limit,
  and delete notes
- [Use skills](skills/) — platform skills, Team skills, and your own skills
- [Protect your data](data/) — what each action keeps, copies of your files,
  and privacy
- [Troubleshoot your Agent](troubleshooting/) — common problems and the default
  limits
- [Frequently asked questions](faq/)

## Integrations

- [Connect Tailscale](tailscale/) — give an Agent a path from your tailnet

Portal administrators [configure Teams](teams/) in Operations. A Team sets
the context, the Agent Policy and Sandbox bindings, the smart-routing models,
and the skills of its Agents.
