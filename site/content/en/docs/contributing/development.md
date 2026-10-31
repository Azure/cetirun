---
title: "Development"
description: "Find the component that you want to change and prepare a local development environment."
weight: 10
---

Select the component that you want to work on. Then follow
[DEVELOPMENT.md]({{< repo "DEVELOPMENT.md" >}}) for its toolchain,
dependency setup, and build commands.

| Component | Source |
| --- | --- |
| Kubernetes operator and custom resources | [Operator]({{< repo "app/cetirun-operator" "tree" >}}) |
| Portal backend and web interface | [Portal]({{< repo "app/cetirun-portal" "tree" >}}) |
| Runtime images and Helm chart | [Runtime chart]({{< repo "app/cetirun-chart" "tree" >}}) |
| Infrastructure and shared services | [Infrastructure]({{< repo "infra" "tree" >}}) |

Keep the operator and portal modules in the same checkout. The portal uses a
local Go module replacement for the operator. Build the frontend before you
build the complete portal binary, so that the binary embeds the real web
assets.

Use [Testing](../../getting-started/local-checks/) to select the local checks
for your change. For site work, follow [Documentation](../site/). A separately
authorized environment is necessary for deployment scripts and live
integrations. They are not part of the local development checks.

Before you open a pull request, follow the
[contribution rules](../) and the generated-file guidance in
[DEVELOPMENT.md]({{< repo "DEVELOPMENT.md" >}}).
