---
title: "Concepts"
description: "The main objects of CetiRun — Agents, Teams, Agent Policies, Sandboxes, and the AI Gateway — and how they relate."
weight: 30
no_list: true
---

CetiRun is an open-source managed runtime for hosting AI agents and AI applications.

A platform administrator installs CetiRun on an AKS cluster. Users then create
Agents in the self-service Portal.

- An **Agent** is one user's long-lived AI agent. It runs one agent type:
  Hermes (the default), OpenClaw, or a Custom image that a platform
  administrator approved. It keeps its files in a private, persistent HOME
  volume. For each Agent, the operator creates a namespace and a StatefulSet
  with at most one Pod.
- A **Team** gives its Agents shared context, default skills, and
  smart-routing models. A **policy-bound Team** also names one
  **Agent Policy** and the **Sandboxes** that its Agents can use. Users can
  select any Team, or no Team, and an Agent Policy applies only to the Agents
  of a Team that has one. Selecting a Team does not add a user to a group or
  share Agents with other users.
- An **Agent Policy** limits what the Agents of a policy-bound Team can use,
  and a **Sandbox** sets how they run. These Agents run only when
  [Sandbox execution](glossary/#sandbox-execution) is on.
- The **AI Gateway** is the model endpoint that the operator configures for
  each Agent. Each Agent has its own key and a monthly budget. The Agent Policy
  of a Sandbox Agent also sets rate limits.
- Users sign in through OAuth2 Proxy with Microsoft Entra ID. The
  **ingress gateway** lets only the owner open an Agent.

Read next:

- [Glossary](glossary/) — one name and one meaning for each term
- [Architecture](architecture/) — components, request paths, and the
  resource model
- [Configuration and skills](configuration/) — how the operator builds the
  configuration of each Agent from deployment values, Teams, and overrides
- [Security](../security/) — trust boundaries, access, credentials, and data
