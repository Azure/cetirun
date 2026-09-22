---
title: "Overview"
description: "CetiRun features, components, and deployment requirements."
weight: 10
no_list: true
---

CetiRun gives an agent a persistent workspace with authenticated access, while
platform operators control its infrastructure and lifecycle.

## Features

- **Agent instances:** OpenClaw or Hermes, a persistent home directory, and
  configurable desktop, terminal, and logging containers.
- **Kubernetes operator:** `Agent` and `Team` resources,
  per-instance namespaces, Helm releases, and Istio routes.
- **Portal:** instance creation and management, provider login, live Copilot
  model selection, and administration of teams.
- **Platform chart:** install the operator and portal together, or use their
  independent charts.
- **Infrastructure:** Bicep templates for AKS and Blob NFS, an Istio
  authentication chart, and deployment scripts.

## Deployment requirements

An Azure subscription and suitable AKS capacity, container and chart artifacts
accessible to that cluster, DNS, TLS, Entra ID configuration, storage, and
operational ownership. Model-provider accounts and entitlements belong to the
users or deploying organization.

CetiRun runs in your organization's infrastructure. Review the
[installation requirements](../getting-started/platform/) and configure
registry access, identity, networking, and storage for your environment.

Authentication depends on the deployed gateway and its trusted identity
headers. Verify namespace isolation, network policies, and the selected
RuntimeClass before granting access. Runtime logging is enabled by default
and can include conversation content. Read [Security](../security/) and
[PRIVACY.md]({{< repo "PRIVACY.md" >}}) before granting access.

## Next steps

Read [architecture](../concepts/architecture/) to understand the request path,
[first instance](../getting-started/first-instance/) if your platform already
exists, or [Security](../security/) before granting access.
