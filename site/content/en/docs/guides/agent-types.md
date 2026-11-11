---
title: "Choose an Agent type"
description: "Compare the Hermes, OpenClaw, and Custom Agent types, and select the type that fits your work."
weight: 6
---

When you create an Agent, you select its type in **Agent Type**. You cannot
change the type later. The form shows only the types that your deployment and
your Team allow.

## Compare the types

| | Hermes | OpenClaw | Custom |
| --- | --- | --- | --- |
| **What it is** | The Hermes agent. This is the default agent type. | The OpenClaw agent. | Your own agent image, which an administrator approves. |
| **Agent UI** | Hermes Webapp. You sign in with the username `cetirun` and the Hermes key. | OpenClaw Control UI. You enter the gateway token and pair each browser one time. | A Web UI, Portal Chat, or no interface. This depends on the image. |
| **Message channels and scheduled jobs** | Yes. Hermes connects to message channels, such as Discord, and runs scheduled jobs. | Yes. OpenClaw connects to message channels, and runs scheduled jobs and heartbeats. | Depends on the image. |
| **Coding agents through ACP** | The platform does not configure them. | Yes. OpenClaw can give tasks to other coding agents. | Depends on the image. |
| **Models** | From the AI Gateway. | From the AI Gateway. | From the AI Gateway. The image selects the model. |
| **Memory** | On by default. | On by default. | Refer to [Use memory](../memory/). |
| **Terminal and Desktop** | Yes. | Yes. | When the deployment configures them for the image. |

## Recommendations

- Select **Hermes** if you want a web application that you sign in to with a key.
  Hermes is the default type.
- Select **OpenClaw** if you want the OpenClaw Control UI, or if you want to
  give coding tasks to other coding agents.
- Select **Custom** only when your administrator gives you an approved image.

Hermes and OpenClaw can both use message channels and scheduled jobs.

## Hermes

A Hermes Agent runs one Hermes gateway process. This process serves the
Webapp, the API, and the message channels. When you close the browser, the
process continues to run. To open the Webapp, refer to
[Open a Hermes Agent](../../getting-started/first-instance/#open-a-hermes-agent).

## OpenClaw

An OpenClaw Agent runs the OpenClaw gateway and its Control UI. To open the
Control UI, refer to
[Open an OpenClaw Agent](../../getting-started/first-instance/#open-an-openclaw-agent).

OpenClaw can start other coding agents through the Agent Client Protocol
(ACP). By default, the allowed coding agents are `claude`, `codex`, and
`opencode`. By default, OpenClaw also approves the tool requests of these
coding agents automatically. Ask your administrator if your deployment changes
these settings.

## Custom

A Custom Agent runs an image that is not Hermes or OpenClaw. Before you create
a Custom Agent, get these values from your administrator:

| Field | What to enter |
| --- | --- |
| **Agent Image** | One of the approved images. The field suggests them. An approved image includes its `sha256` digest, for example `registry.example.com/team/agent@sha256:<digest>`. The portal does not accept a tag or an image that is not approved. When no image is approved, the form shows a warning, and you cannot create a Custom Agent. |
| **Agent Username** | A Linux user that exists in the image and is not `root`. The home directory is `/home/<username>`. You cannot change the username later. |
| **Serve Web UI** | On, if the image serves a browser interface. The portal then shows **Web UI** for the Agent. The image must serve `/ui/<agent-name>/` and `/healthz`. |
| **Web UI Port** or **HTTP Port (optional)** | The port of the Web UI, or the port of the Chat API. Leave the port empty for an Agent without an HTTP endpoint. |

A Custom Agent with an HTTP port and without a Web UI can use Portal Chat, if
your deployment allows it. Refer to [Chat with a Custom Agent](../custom-chat/).
The form does not offer a gateway token, a Tailscale key, or a default model
for a Custom Agent.

Sources: [creation form]({{< repo "app/cetirun-portal/web/src/components/CreateForm.tsx" >}}),
[runtime chart values]({{< repo "app/cetirun-chart/values.yaml" >}}),
and [Agent Harness](../../reference/agent-harness/#custom-runtime).
