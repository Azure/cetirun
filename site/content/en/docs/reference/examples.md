---
title: "Examples"
description: "Example Custom Agents and native Sandbox walkthroughs, with what each needs and where to start."
weight: 40
---

The [`examples/`]({{< repo "examples" "tree" >}}) directory contains Custom
Agent images and a native Sandbox walkthrough. Use them to try CetiRun or as a
start for your own image. Each example has a README with its full steps.

| Example | What it shows | Runs offline | Needs a cluster | Start here |
| --- | --- | --- | --- | --- |
| Pi Agent | A Custom Agent with the Pi coding agent, a terminal UI, and an OpenAI-compatible HTTP adapter for Portal Chat | Node tests | Yes, to deploy the image | [README]({{< repo "examples/pi-agent/README.md" >}}) |
| VS Code Server | A Custom Agent with a VS Code Server Web UI and Copilot Chat through the AI Gateway | Node tests | Yes, to deploy the image | [README]({{< repo "examples/vscode-server/README.md" >}}) |
| Coding agents | GitHub Copilot CLI, Claude Code, and Codex CLI as three Custom Agents | Adapter tests | Yes, to deploy the images | [README]({{< repo "examples/coding-agents/README.md" >}}) |
| Coding runtime mock | An offline demonstration of the Custom Chat interface with fixed synthetic results | Yes | No | [README]({{< repo "examples/coding-agents/examples/coding-runtime/README.md" >}}) |
| Native Sandbox first run | One OpenClaw Agent in a Kata Sandbox with a shared AI Gateway model, a network comparison, and cleanup | No | Yes, a cluster with a registered Sandbox platform | [README]({{< repo "examples/native-sandbox/README.md" >}}) |
| Local Sandbox validation | A platform probe for a local Kind cluster with Kata and host-path CSI | No | A disposable local Kind cluster that you own | [README]({{< repo "examples/native-sandbox/local/README.md" >}}) |
| AKS Sandbox checks | Read-only Kubernetes API checks of an AKS target | No | Yes, an authorized AKS cluster | [README]({{< repo "examples/native-sandbox/aks/README.md" >}}) |

## Requirements for Custom Agent examples

- An administrator enables Custom provisioning and adds the exact image digest
  to `customRuntime.approvedImages` of the Operator. For the image contract,
  refer to [Custom runtime](../agent-harness/#custom-runtime).
- The image needs a registry that the cluster can pull from. Build and publish
  the image yourself, or use a published release image that your administrator
  approved.
- The VS Code Server example needs your acceptance of the
  [VS Code Server license](https://code.visualstudio.com/license/server)
  before you build the image.
- In the coding agents example, each Agent uses the model routes that the AI
  Gateway authorizes for it. Some routes need a separate provider account.

## Requirements for native Sandbox examples

- An administrator registers the Sandbox platform and enables Sandbox
  execution in the Operator. Refer to
  [Policy-bound Teams](../../operations/policy-teams/).
- A Team binds the Sandbox to an AgentPolicy that allows the Agent's model.
- The local validation needs Docker, Kind, and QEMU/KVM on a workstation that
  you own. Do not use it on AKS.

Offline tests do not show that an example runs on a cluster. To validate an
example on a cluster, get authorization for the target environment first.
