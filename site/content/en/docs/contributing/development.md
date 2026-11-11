---
title: "Prepare a development environment"
linkTitle: "Development"
description: "Install the tools for the component that you change, download its dependencies, and find its checks."
weight: 10
---

[DEVELOPMENT.md]({{< repo "DEVELOPMENT.md" >}}) is the developer guide. Use
it for the commands in these steps.

1. Find the component that you want to change in the
   [repository layout]({{< repo "DEVELOPMENT.md" >}}#repository-layout). For
   what each component does, refer to [Architecture](../../concepts/architecture/).
2. Install the tools for that component. Refer to
   [Tools and dependencies]({{< repo "DEVELOPMENT.md" >}}#tools-and-dependencies).
3. Download the Go, npm, and site dependencies one time. This step needs
   network access. The checks after it run offline.
4. Find the checks for the paths that you change. Refer to
   [Checks for each change]({{< repo "DEVELOPMENT.md" >}}#checks-for-each-change)
   and [Run local checks](../../getting-started/local-checks/).

Keep all Go modules in one checkout. The Operator, the Portal, the Agent
Identity Broker, and Connections MCP use local `replace` directives for the
shared `app/internal` packages and the Operator API types. Build the Portal
frontend before you build the complete Portal binary, so that the binary
embeds the real web assets.

To start the Portal or the Operator on your workstation, refer to
[Run components locally]({{< repo "DEVELOPMENT.md" >}}#run-components-locally).
These processes connect to the cluster of your current kubeconfig context. To
add a new agent type, refer to
[Add an agent integration]({{< repo "DEVELOPMENT.md" >}}#add-an-agent-integration).

Deployment scripts and live integration tests need a separately authorized
environment. They are not part of the local checks. Refer to
[Integration and E2E tests]({{< repo "DEVELOPMENT.md" >}}#integration-and-e2e-tests).
For site work, follow [Contribute to documentation](../site/).

Before you open a pull request, follow the [contribution rules](../) and the
[generated-file guidance]({{< repo "DEVELOPMENT.md" >}}#generated-files-and-dependencies).
