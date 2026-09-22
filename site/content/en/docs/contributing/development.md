---
title: "Development"
description: "Find the component you want to change and set up a local development environment."
weight: 10
---

Choose the component you want to work on, then follow
[DEVELOPMENT.md]({{< repo "DEVELOPMENT.md" >}}) for its toolchain,
dependency setup, and build commands.

| Component | Source |
| --- | --- |
| Kubernetes operator and custom resources | [Operator]({{< repo "app/cetirun-operator" "tree" >}}) |
| Portal backend and web interface | [Portal]({{< repo "app/cetirun-portal" "tree" >}}) |
| Runtime images and Helm chart | [Runtime chart]({{< repo "app/cetirun-chart" "tree" >}}) |
| Infrastructure and shared services | [Infrastructure]({{< repo "infra" "tree" >}}) |

Keep the operator and portal modules in the same checkout: the portal uses a
local Go module replacement for the operator. Build the frontend before
building the complete portal binary so that it embeds the real web assets.

Use [Testing](../../getting-started/local-checks/) to select local checks for
your change. For site work, follow [Documentation](../site/).
Deployment scripts and live integrations require a separately authorized
environment; they are not part of local development checks.

Before opening a pull request, follow the
[contribution requirements](../) and the generated-file guidance in
[DEVELOPMENT.md]({{< repo "DEVELOPMENT.md" >}}).
