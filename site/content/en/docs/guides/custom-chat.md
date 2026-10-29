---
title: "Chat with a Custom Agent"
description: "Use a private Custom Chat endpoint and keep conversation history in your browser."
weight: 45
---

Portal Chat is available for Custom Agents that set `spec.agent.port` and leave
`spec.agent.webUI` false. Hermes and OpenClaw keep their native interfaces;
Custom Web UI and headless instances do not use this Chat page.

The Portal authenticates each request, checks instance ownership and the applied
runtime, then calls the private Agent Service. It does not call a model provider
on the Agent's behalf, execute tools, or manage image-specific login.

## Enable the feature

Chat is disabled by default. Deploy compatible Portal, Operator and runtime
charts before enabling it. The runtime chart must advertise
`cetirun.microsoft.com/portal-chat: v1`; older pinned charts keep their existing
behavior and do not gain Chat access automatically.

For the platform umbrella, one setting enables Portal Chat and configures its
private runtime caller:

```yaml
global:
  portalChat:
    enabled: true
    identityRealm: "entra:YOUR_TENANT_ID"
```

Replace the realm with a stable identifier for your authentication provider and
tenant. It is not a credential. Keep it unchanged across Portal replicas and
normal upgrades; changing it selects a different browser-history partition.
The trusted authentication gateway must supply its opaque
`X-Auth-Request-User` identity header. Chat does not require Agent Identity or
its Broker, and it does not fall back to an alias when that subject is missing.

This setting does not enable Custom provisioning, approve an image, add a Team
entitlement, or release a Sandbox hold. Keep those existing controls in place.
The Portal must be served over HTTPS, with its configured `PORTAL_ORIGIN`
matching the browser origin.

For independently installed components, set `chat.enabled` and
`chat.identityRealm` on the Portal chart, then configure matching callers on the
Operator chart. For example, if the Portal release is named `portal-release`
in namespace `platform-system`:

```yaml
chatAccess:
  enabled: true
  callers:
    - namespace: platform-system
      podLabels:
        app.kubernetes.io/name: cetirun-portal
        app.kubernetes.io/instance: portal-release
```

Use the actual Portal Pod labels. Enabling Chat adds Portal read access to
Services, NetworkPolicies, CiliumNetworkPolicies and private VirtualServices;
Chat itself reads no Secrets and performs no exec or workload writes.
For governed Custom, `chat.sandboxStateNamespace` selects the Operator namespace
(by default the configured Gateway catalog namespace, normally `cetirun-system`)
and grants read-only access to its execution ConfigMaps.
Portal verifies the current Agent intent, owned namespace/workload/Service,
complete API-defaulted workload template, applied Cilium caller rule and private
route before forwarding. Held, stale or mismatched execution cannot use the
non-Sandbox Chat path as a fallback.

Operator configures the private caller through the common policy compiler or,
without Sandbox selection, the existing Custom bindings. It does not publish the
Chat port on the public gateway. A common-policy caller change participates in
normal policy reprojection; non-Sandbox binding-only caller changes do not
replace an unchanged Agent Pod.

Portal and Agent Web UIs can use paths on the same hostname. Account and Agent
UID partitions prevent normal Chat operations from mixing records, but paths
and database names do not isolate same-origin scripts. A Custom Web UI on that
origin can access the underlying browser storage. Deployments using local
history accept this shared browser trust boundary; protection against untrusted
same-origin applications requires a separate origin and is not provided here.
Preserve the gateway-only Portal access restrictions and qualify the actual
NetworkPolicy and mesh path before opening access.

## Start a conversation

Open an eligible instance and select **Chat**. Select a model identifier returned
by that Agent, enter a message, and use **Send** or Ctrl+Enter / Command+Enter.
Enter inserts a newline; input-method composition does not submit a message.
An empty or unavailable model list is separate from HTTP readiness. Complete
model setup through the image's supported workflow, not through a Portal Chat
credential field. For a Gateway-backed image, use **Sign in with GitHub** in
the instance Overview for private Copilot access, or an administrator-provided
shared model. Chat continues to call the Agent, which calls the AI Gateway
with its own Virtual Key; no model credential is sent to the browser.

The Chat page provides local conversation creation, selection, rename, deletion
and text copying. Replies appear incrementally. **Stop** stops receiving and
propagates cancellation; it does not prove that tools stopped, undo side effects,
revoke model access or stop provider billing.

Sending and conversation selection stay disabled until local history finishes
updating. Leaving Chat stops receiving; local saves already in progress may
finish, but late results do not update a replacement Chat page or send another
request.

Interrupted, failed and incomplete replies retain their actual state. Refreshing
does not resend a request. Review uncertain results before explicitly starting
a new turn. A new Chat conversation starts a new UI transcript, not a new Agent:
the same Agent still owns its workspace, tools and private memory.

## Where history lives

History and drafts live in IndexedDB on the current browser profile and Portal
origin. Portal has no history database, history PVC, background queue or cloud
sync. It passes the selected prior messages with each request, so an Agent with
no native history API can still receive multi-turn context.

Records are partitioned by the authenticated account and actual Agent UID.
Same-name replacement instances do not inherit old records. **Local history**
also provides read-only access to records whose instance was removed or whose
Chat endpoint is no longer available. A local record never grants server access.

Signing out retains local history by default. The sign-out dialog can clear this
account's local history first. Clearing it does not remove Agent files, native
sessions, memory, another account's records or other Portal preferences.

Browser data can be lost when site data is cleared, a private-browsing session
ends, the browser evicts storage, or a device fails. Local history is not a
backup. There is no application-level encryption; use a separate browser profile
on shared devices and clear records when appropriate. The platform cannot
remotely erase all copies already saved in browsers.

## Limits and failures

The initial implementation keeps up to 100 conversations and 50 MiB of text and
drafts per account, with 2,000 messages per conversation. It asks the user to
remove records when full instead of silently deleting the oldest conversations.
Storage errors stop new sends and remain visible; the Portal does not silently
switch to temporary memory storage.

Each request includes at most 49 complete prior message pairs plus the new
input, within a 1 MiB encoded body. The current input is limited to 64 KiB of
UTF-8 text. The page reports omitted context without deleting stored history.
There is no automatic summary, continuation or retry after a context-limit error.
Replies are bounded to 4 MiB, with a ten-minute request limit and shorter connect,
first-response and idle limits. The Agent may impose lower limits.

Browser locks prevent concurrent Portal Chat sends to the same Agent from tabs
in the same profile. They do not coordinate different devices. Server connection
limits are per Portal replica, not a global execution queue or an exactly-once
guarantee. Busy Agents must report their own concurrency limits.

Operator creates an Agent-owned mesh-only VirtualService with retries disabled
for the private Chat Service. Portal verifies this route before forwarding;
request headers alone cannot override every mesh's retry behavior. The incoming
Chat route also disables retries. Any additional proxy in the deployment must
preserve this no-replay behavior.

Suspended, deleting, held, unready or not-yet-applied instances cannot accept new
Chat requests. The page can still display locally saved history. A missing
private network path, incompatible chart or changed instance binding is reported
instead of falling back to another URL, model or Agent.

Chat requires a secure browser context with IndexedDB, Web Locks,
BroadcastChannel and streaming fetch. A database-version conflict preserves the
data and asks for compatible Portal code or a refreshed tab; it does not delete
the database to recover.

See [Agent Harness](../../reference/agent-harness/#chat) for the image HTTP
contract and [Lifecycle](../lifecycle/) for instance operations. Browser,
authentication, network and real-model qualification are separate checks;
rendering a chart or passing a synthetic conversation test does not establish
production readiness.
