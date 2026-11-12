---
title: "Enable Portal Chat"
description: "Enable the private Portal Chat path for Custom Agents, and review its routing, validation, and browser-storage boundaries."
weight: 39
---

Portal Chat lets the owner of a Custom Agent chat with it in the Portal when
the Agent has no native Web UI. This page is for platform administrators. For
the user tasks, refer to [Chat with a Custom Agent](../../guides/custom-chat/).

Portal Chat is available for Custom Agents that set `spec.agent.port` and leave
`spec.agent.webUI` false. The Portal authenticates each request and validates
the Agent ownership and the applied runtime. Then it calls the private Agent
Service. It does not call a model provider on the Agent's behalf, execute
tools, or manage image-specific login.

## Enable the feature

Chat is disabled by default. Before you enable it, deploy compatible Portal,
Operator and runtime charts. The runtime chart must advertise
`cetirun.microsoft.com/portal-chat: v1`. Older pinned charts keep their
behavior and do not get Chat access automatically.

For the platform umbrella, one setting enables Portal Chat and configures its
private runtime caller:

```yaml
global:
  portalChat:
    enabled: true
    identityRealm: "entra:YOUR_TENANT_ID"
```

Replace the realm with a stable identifier for your authentication provider and
tenant. The realm is not a credential. Keep it unchanged across Portal replicas
and normal upgrades. A change to the realm selects a different browser-history
partition.

The trusted authentication gateway must supply its opaque
`X-Auth-Request-User` identity header. Agent Identity and its Broker are not
necessary for Chat. If that subject is not present, Chat does not use an alias
as a fallback.

This setting does not enable Custom provisioning, approve an image, add a Team
entitlement, or release a Sandbox hold. Keep those controls in place. The
Portal must be served over HTTPS, and its configured `PORTAL_ORIGIN` must match
the browser origin.

For components that are installed independently, set `chat.enabled` and
`chat.identityRealm` on the Portal chart. Then, on the Operator chart, configure
callers that match the Portal. For example, if the Portal release is named
`portal-release` in namespace `platform-system`, use this configuration:

```yaml
chatAccess:
  enabled: true
  callers:
    - namespace: platform-system
      podLabels:
        app.kubernetes.io/name: cetirun-portal
        app.kubernetes.io/instance: portal-release
```

Use the actual Portal Pod labels. If you enable Chat, the Portal gets read
access to Services, NetworkPolicies, CiliumNetworkPolicies and private
VirtualServices. Chat itself reads no Secrets, and it does no exec operations
and no workload writes.

For governed Custom, `chat.sandboxStateNamespace` selects the Operator
namespace. By default, this is the configured Gateway catalog namespace, which
is usually `cetirun-system`. The setting also grants read-only access to the
execution ConfigMaps in that namespace.

## Request validation and routing

Before the Portal forwards a request, it validates these items:

- The current Agent intent
- The owned namespace, workload, and Service
- The complete API-defaulted workload template
- The applied Cilium caller rule
- The private route

Held, stale or mismatched execution cannot use the non-Sandbox Chat path as a
fallback.

Operator configures the private caller through the common policy compiler.
Without Sandbox selection, Operator configures it through the Custom bindings
that already exist instead. It does not publish the Chat port on the public
gateway. A common-policy caller change is part of normal policy reprojection.
Non-Sandbox caller changes that affect only bindings do not replace an
unchanged Agent Pod.

Operator creates an Agent-owned mesh-only VirtualService with retries disabled
for the private Chat Service. Request headers alone cannot override the retry
behavior of every mesh. The inbound Chat route also disables retries. Any
other proxy in the deployment must keep this no-replay behavior.

Server connection limits apply to each Portal replica. They are not a global
execution queue or an exactly-once guarantee. Busy Agents must report their own
concurrency limits.

## Browser storage boundary

Portal and Agent Web UIs can use paths on the same hostname. In normal Chat
operations, account and Agent UID partitions keep records separate. But paths
and database names do not isolate same-origin scripts. A Custom Web UI on that
origin can access the browser storage that holds these records.

A deployment that uses local history accepts this shared browser trust
boundary. To protect against untrusted same-origin applications, a separate
origin is necessary. This feature does not supply that protection. Keep the
gateway-only Portal access restrictions. Before you open access, validate the
actual NetworkPolicy and mesh path.

The platform cannot remotely remove the copies of the history that browsers
already saved.

## Qualification

For the image HTTP contract, see [Agent Harness](../../reference/agent-harness/#chat).
Browser, authentication, network and real-model qualification are separate
checks. A rendered chart or a passed synthetic conversation test does not
establish production readiness.
