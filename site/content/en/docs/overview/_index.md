---
title: "Overview"
description: "Run long-lived OpenClaw, Hermes, and Custom Agents for many users on AKS, with one isolated namespace for each Agent and governed model access."
weight: 10
no_list: true
---

CetiRun hosts long-lived AI agents for the people in your organization. Users
create Agents in a self-service Portal, and each Agent keeps its files in a
private, persistent HOME volume. Platform administrators install CetiRun on
Azure Kubernetes Service (AKS) in their own Azure subscription. They decide
which agent types, models, skills, and network destinations the Agents can use.

When each person runs OpenClaw or Hermes on their own computer, each person
manages one agent and its model keys. CetiRun runs these agents, and your own
agent images, for many users on one shared AKS cluster. For the terms on this
page, read the [Glossary](../concepts/glossary/).

## Features

- **Agents:** Each Agent runs one agent type: Hermes (the default), OpenClaw,
  or a Custom image that a platform administrator approved. A Custom Agent can serve a
  chat API, a browser application, or no HTTP interface. Each Agent has a
  private HOME volume, and it can have a terminal and a browser desktop.
- **Access:** Users sign in with Microsoft Entra ID. The ingress gateway lets
  only the owner open the Agent UI, chat, terminal, and desktop of an Agent.
  This rule needs a unique alias for each user. The alias is the part of the
  sign-in name before `@`.
- **Model access:** The operator configures each Agent to call models through
  the AI Gateway, which is based on LiteLLM. Each Agent has its own key and a
  monthly budget in US dollars. The Agent Policy of a Sandbox Agent also sets
  rate limits. An owner can also sign in to GitHub Copilot for an Agent.
  Smart routing sends simple requests to cheaper models and hard requests to
  stronger models.
- **Governance:** Portal administrators create Teams, Agent Policies, and
  Sandboxes. A policy-bound Team limits the agent types, skills, models,
  budget, network access, and identity of its Agents. Its Agents run in a
  Sandbox with Kata or container isolation when Sandbox execution is on.
- **Skills and memory:** A Team gives its Agents skills. The optional Skill
  Marketplace publishes approved skills from one Git repository. Memory, which
  is on by default, keeps the notes of each Agent in its HOME.
- **Connections and identity:** An owner gives an Agent access to GitHub,
  Google, or Azure. The Agent Identity Broker keeps the credentials, and the
  Agent uses the services through Connections MCP. An Agent can also get its
  own Microsoft Entra Agent Identity.
- **Operations:** `make deploy` installs the platform. Portal administrators
  see all Agents with their status and events.
  [CetiAdmin](../operations/cetiadmin/), an operations Agent that is on by
  default, can read the Secrets of each standard Agent and run commands in its
  Pod.
  The ADX-Mon collection, which `make deploy` installs, sends logs, events,
  and metrics to Azure Data Explorer.

The Kubernetes API has the cluster-scoped resources `Agent`, `Team`,
`Sandbox`, and `AgentPolicy`. For the components and how they work together,
read [Architecture](../concepts/architecture/).

## Two kinds of Agents

When users create an Agent, they can select any Team or **(none)**. An Agent
Policy applies only to the Agents of a policy-bound Team, so it does not limit
all users. A standard Agent gets the deployment defaults and no rate limits. A
Sandbox Agent starts only when a platform administrator enables Sandbox
execution, which `make deploy` does not do. For the comparison, read
[Two kinds of Agents](../concepts/architecture/#two-kinds-of-agents).

## Deployment requirements

Before you install CetiRun with `make deploy`, prepare these items. For the
complete list, read
[Prepare the environment](../getting-started/platform/#prepare-the-environment).

- **AKS cluster:** Kubernetes 1.37 or later, managed Istio with an external
  ingress gateway, Kata node pools, Azure CNI powered by Cilium, and Workload
  Identity.
- **Microsoft Entra ID:** an application for the Portal sign-in, and the Agent
  Identity resources that `make deploy` requires.
- **Certificates:** certificates for the Broker, Connections MCP, and Sandbox
  admission, and a TLS certificate for the apex domain.
- **Models:** model deployments and keys for the AI Gateway. The default smart
  routing also needs a deployment of the classifier model. Your organization
  supplies the model-provider accounts. An owner who uses GitHub Copilot uses
  their own GitHub account.
- **Storage:** StorageClasses for HOME and for the AI Gateway data, and Azure
  Blob storage for shared skills.
- **Artifacts:** a container registry from which the cluster can pull the
  CetiRun images and charts.
- **DNS:** a DNS name for the apex domain.
- **Observability:** an Azure Data Explorer cluster with the logs, metrics,
  and Agent logs databases of the ADX-Mon collection, and the managed identity
  of its ingestor. By default, the cluster template creates them.
- **CetiAdmin:** its owner, its monthly budget, and its access to the
  Kubernetes API. To install without CetiAdmin, set `cetiadmin.enabled: false`.
- **Operations:** the people who operate the deployment, and their Azure and
  cluster permissions.

## Before you give users access

> **Warning:** By default, CetiRun keeps conversation content in the AI Gateway
> database, in container logs, and in the memory notes of each Agent. CetiRun
> does not redact these stores. Before you give users access, restrict who can
> read them. [Data collection](../security/#data-collection) lists the stores.

Also check how namespaces, network policies, and the RuntimeClass isolate the
Agents in your deployment. For the controls and their limits, read
[Security](../security/) and [PRIVACY.md]({{< repo "PRIVACY.md" >}}).

## Next steps

- To evaluate the design, read [Architecture](../concepts/architecture/) and
  [Security](../security/).
- To install CetiRun, follow [Install CetiRun](../getting-started/platform/).
- To use a deployment that already exists,
  [create your first Agent](../getting-started/first-instance/).
