# CetiRun Agent Identity Broker

The Agent Identity Broker is the trusted execution boundary between CetiRun
agent workloads, the Microsoft Entra Agent ID Auth SDK, and approved downstream
services.

It is intentionally more than a credential store. The broker owns the OBO
authorization session, validates the calling workload and instance binding,
asks the Auth SDK for a short-lived downstream authorization header, executes
an allowlisted typed operation, and returns a minimized response. Downstream
authorization headers and OAuth tokens are never returned to an agent
workload.

The initial tool surface contains read-only Azure subscription, resource group,
and Resource Graph operations. It is not a general ARM proxy: callers cannot
choose an arbitrary URL, HTTP method, API version, authorization header, or KQL
query.

The broker and the Microsoft Auth SDK are separate containers in one Pod. This
keeps the vendor authentication component isolated while avoiding a reusable
credential protocol between independently reachable services.
