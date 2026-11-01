---
title: "Overview"
description: "This page describes the CetiRun features and components, and the items that a deployment must have."
weight: 10
no_list: true
---

CetiRun gives an agent a persistent workspace with authenticated access.
Platform operators control its infrastructure and lifecycle.

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

A deployment must have an Azure subscription, suitable AKS capacity, and
container and chart artifacts that the cluster can access. It must also have
DNS, TLS, Entra ID configuration, storage, and operational ownership. The
model-provider accounts and entitlements belong to the users or to the
organization that deploys CetiRun.

CetiRun runs in your organization's infrastructure. Review the
[installation prerequisites](../getting-started/platform/). Then configure
registry access, identity, networking, and storage for your environment.

Authentication depends on the deployed gateway and its trusted identity
headers. Before you grant access, examine the namespace isolation, the network
policies, and the selected RuntimeClass. Runtime logging is enabled by default
and can include conversation content. Before you grant access, read
[Security](../security/) and [PRIVACY.md]({{< repo "PRIVACY.md" >}}).

## Next steps

To understand the request path, read [architecture](../concepts/architecture/).
If your platform already exists, read
[first instance](../getting-started/first-instance/). Before you grant access,
read [Security](../security/).
