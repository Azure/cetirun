---
title: "Concepts"
description: "Understand instances, teams, and deployments in CetiRun."
weight: 30
no_list: true
---

CetiRun is an open-source managed runtime for hosting AI agents and AI applications.

An **Agent** is a long-lived workspace that uses OpenClaw or Hermes.
An **instance** is a deployed Agent workspace.
A **Team** supplies shared context and defaults. A Team is not a membership
or authorization policy. Users can select any Team, or no Team, and an Agent
Policy applies only to the Agents of a Team that has one. A **deployment**
combines the operator, portal, infrastructure, and policies that make those
workspaces available to users.

For the responsibilities of each component, read [Architecture](architecture/).
For access controls and data protection, read [Security](../security/).
