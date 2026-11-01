---
title: "Chat with a Custom Agent"
description: "Use a private Custom Chat endpoint and keep conversation history in your browser."
weight: 45
---

Portal Chat is available for Custom Agents that set `spec.agent.port` and leave
`spec.agent.webUI` false. Hermes and OpenClaw keep their native interfaces.
Custom Web UI and headless instances do not use this Chat page.

The Portal authenticates each request and validates the instance ownership and
the applied runtime. Then it calls the private Agent Service. It does not call a
model provider on the Agent's behalf, execute tools, or manage image-specific
login.

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

Portal and Agent Web UIs can use paths on the same hostname. In normal Chat
operations, account and Agent UID partitions keep records separate. But paths
and database names do not isolate same-origin scripts. A Custom Web UI on that
origin can access the browser storage that holds these records.

A deployment that uses local history accepts this shared browser trust
boundary. To protect against untrusted same-origin applications, a separate
origin is necessary. This feature does not supply that protection. Keep the
gateway-only Portal access restrictions. Before you open access, validate the
actual NetworkPolicy and mesh path.

## Start a conversation

Open an eligible instance. Then select **Chat**. Select a model identifier that
the Agent returns. Enter a message. Then use **Send** or
Ctrl+Enter / Command+Enter. The Enter key inserts a newline, and input-method
composition does not submit a message.

An empty or unavailable model list is separate from HTTP readiness. Complete
the model setup through the supported workflow of the image, not through a
Portal Chat credential field. For a Gateway-backed image, use
**Sign in with GitHub** in the instance Overview for private Copilot access. As
an alternative, use a shared model that an administrator supplied. Chat
continues to call the Agent, which calls the AI Gateway with its own Virtual
Key. No model credential is sent to the browser.

The Chat page lets you create, select, rename, and delete local conversations
and copy text. Replies appear incrementally. **Stop** stops the reception of
the reply and propagates the cancellation. It does not prove that tools stopped.
It also does not undo side effects, revoke model access, or stop provider
billing.

The send action and conversation selection stay disabled until local history
completes its update. If you leave Chat, the reception of the reply stops.
Local saves that already started can finish. But late results do not update a
replacement Chat page or send another request.

Interrupted, failed and incomplete replies keep their actual state. A page
refresh does not resend a request. Before you explicitly start a new turn,
examine uncertain results. A new Chat conversation starts a new UI transcript,
not a new Agent. The same Agent still owns its workspace, tools and private
memory.

## Where history lives

History and drafts live in IndexedDB on the current browser profile and Portal
origin. Portal has no history database, history PVC, background queue or cloud
sync. It passes the selected prior messages with each request. As a result, an
Agent with no native history API can still receive multi-turn context.

Records are partitioned by the authenticated account and the actual Agent UID.
A replacement instance with the same name does not inherit old records.
**Local history** also gives read-only access to records whose instance was
removed or whose Chat endpoint is no longer available. A local record never
grants server access.

By default, local history stays after you sign out. The sign-out dialog can
clear the local history of this account first. If the dialog clears it, this
does not remove Agent files, native sessions, memory, another account's records
or other Portal preferences.

Browser data can be lost when site data is cleared, a private browser session
ends, the browser evicts storage, or a device fails. Local history is not a
backup. There is no application-level encryption. On shared devices, use a
separate browser profile. Clear records when applicable. The platform cannot
remotely remove all copies that are already saved in browsers.

## Limits and failures

The initial implementation keeps up to 100 conversations and 50 MiB of text and
drafts per account, with 2,000 messages per conversation. When the local history
is full, it asks the user to remove records. It does not silently delete the
oldest conversations. Storage errors stop new sends and stay visible. The
Portal does not silently switch to temporary memory storage.

Each request includes at most 49 complete prior message pairs and the new
input, in an encoded body of at most 1 MiB. The current input is limited to
64 KiB of UTF-8 text. The page reports omitted context, but it does not delete
stored history. There is no automatic summary, continuation or retry after a
context-limit error.

Replies are limited to 4 MiB. The request limit is ten minutes. The connect,
first-response and idle limits are shorter. The Agent can apply lower limits.

Browser locks prevent concurrent Portal Chat sends to the same Agent from tabs
in the same profile. They do not coordinate different devices. Server
connection limits apply to each Portal replica. They are not a global execution
queue or an exactly-once guarantee. Busy Agents must report their own
concurrency limits.

Operator creates an Agent-owned mesh-only VirtualService with retries disabled
for the private Chat Service. Before the Portal forwards a request, it validates
this route. Request headers alone cannot override the retry behavior of every
mesh. The inbound Chat route also disables retries. Any other proxy in the
deployment must keep this no-replay behavior.

Instances that are suspended, in deletion, held, not ready, or not yet applied
cannot accept new Chat requests. The page can still show locally saved
history. If there is no private network path, the chart is incompatible, or the
instance binding changed, Chat reports the problem. Chat does not use another
URL, model, or Agent as a fallback.

A secure browser context with IndexedDB, Web Locks, BroadcastChannel and
streaming fetch is necessary for Chat. When a database-version conflict occurs,
Chat keeps the data and asks for compatible Portal code or a refreshed tab.
Chat does not delete the database to recover.

For the image HTTP contract, see [Agent Harness](../../reference/agent-harness/#chat).
For instance operations, see [Lifecycle](../lifecycle/). Browser,
authentication, network and real-model qualification are separate checks. A
rendered chart or a passed synthetic conversation test does not establish
production readiness.
