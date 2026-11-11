---
title: "Connect Tailscale"
description: "Give an Agent an address on your tailnet, and know which sign-in protects that address."
weight: 30
---

Tailscale gives your Agent an address on your own tailnet. You can then reach
the Agent from your devices on the tailnet. The portal address of the Agent
continues to work.

> **Warning:** On the tailnet, the portal does not check your Microsoft Entra
> sign-in or the ownership of the Agent. Each device that the tailnet ACLs
> allow can connect to the Agent. The gateway token and the Hermes API key in
> the [sign-in table](#connect) protect only the OpenClaw gateway and the
> Hermes API. By default, the terminal (port 7681) and the desktop (ports 6080
> and 5900) of the Agent also accept connections on its tailnet address. They
> do not ask for a token, a key, or a password.
>
> Before you add Tailscale, ask your tailnet administrator to limit the ACLs
> so that only your own devices can reach the Agent, on all ports. A limit on
> the HTTPS port only is not sufficient when other rules allow the other ports.

## Before you begin

- Make sure that your deployment allows Tailscale. If the Agent form has no
  **Tailscale Auth Key (optional)** field, ask your administrator.
- Get access to a tailnet, and install the Tailscale client on your device.
- An Agent of a policy-bound Team runs in a Sandbox. Tailscale is not
  available in this Sandbox.
- Tailscale is available for Hermes and OpenClaw Agents.

## Prepare an auth key

1. In the administration console of your tailnet, create an auth key. Use a
   reusable, ephemeral key, because the platform can create the Pod of the
   Agent again. A single-use key can stop working after the first connection.
2. With your tailnet administrator, examine the ACLs, the key expiry, the
   device approval, and the HTTPS settings of the tailnet.

The Tailscale state of the Agent is not in the home directory. As a result,
the Agent sometimes must register on the tailnet again after the platform
creates its Pod again.

Keep the key private. Do not put it into a manifest, the shell history, a
screenshot, or an issue.

## Add Tailscale to your Agent

1. In the portal, select **New Agent**, or open your Agent and select
   **Edit Agent**.
2. Enter the key in **Tailscale Auth Key (optional)**. In the edit dialog, the
   field is **Tailscale Auth Key**.
3. Select **Create Agent** or **Save**. When you save a change to an Agent that
   exists, the Agent restarts.

The **Overview** tab then shows **Tailscale Auth Key**. You can show or copy
the key there. The hidden value is a display feature, not access control.

To replace the key, enter a new key in **Edit Agent**. If you leave the field
empty, the key does not change. To remove Tailscale, select
**Disable Tailscale** in **Edit Agent**. The removal does not revoke the key
in the tailnet. Ask your tailnet administrator to revoke it.

## Connect

1. Sign in to the tailnet on your device.
2. In the Tailscale machines list, find the Agent. Its name is usually
   `cetirun-0`. If the name is already in use on the tailnet, Tailscale adds a
   suffix. Always use the name that Tailscale shows.
3. Open the address for your Agent type:

| Agent type | Address | Sign-in |
| --- | --- | --- |
| OpenClaw | `https://<node-name>.<tailnet>.ts.net/ui/<agent-name>/` | Enter the gateway token. Then pair the browser, as in [Open an OpenClaw Agent](../../getting-started/first-instance/#open-an-openclaw-agent). |
| Hermes | `https://<node-name>.<tailnet>.ts.net` | This address serves the Hermes API, not the Webapp. Use it with an API client, and authenticate with the **Hermes API / Webapp Key**. |

To use the Hermes Webapp, open it through the portal.

## If it does not connect

On the **Status** tab, find the `TailscaleReady` row. `TailscaleReady` tells
you that the platform prepared the key. It does not tell you that a client
connected. Then examine these items with your tailnet administrator: the key
expiry and reuse, the device authorization, the tailnet ACLs, and the network
reachability.

Do not make the Agent public to avoid a tailnet problem. If someone sees
your key, replace it in **Edit Agent**, and ask your tailnet administrator to
revoke the old key.

## Administrator tasks

### Enable Tailscale in the portal

The portal shows the Tailscale fields only when the portal chart has
`features.tailscaleEnabled: true` (default `false`). When the flag is off, the
portal API also rejects key submissions that are not empty. The flag does not
disable Tailscale in Agents that are already configured. It also does not
prevent use of the Kubernetes path.

The portal creates `tailscale-auth` in the Agent namespace and records
`spec.tailscale.authKeySecretRef`. The portal editor writes `tailscale-auth`.
Do not use the portal editor to rotate a differently named Secret that was
supplied through Kubernetes. A custom reference that exists can stay
unchanged.

### Add Tailscale with kubectl

These steps change cluster resources. They need permission to create Secrets
in the Agent namespace and to change Agent resources. Portal users use
**Edit Agent** instead.

For an Agent named `alice` that exists, put the key in the protected local file
`tailscale-auth-key`. Do not put a newline at the end of the file. Then run
these commands:

```bash
kubectl create secret generic tailscale-auth -n cetirun-alice \
  --from-file=TAILSCALE_AUTH_KEY=./tailscale-auth-key
kubectl patch agents.cetirun.microsoft.com alice --type=merge \
  -p '{"spec":{"tailscale":{"authKeySecretRef":"tailscale-auth"}}}'
```

For a new Agent, an authorized platform operator must create its
`cetirun-<agent-name>` namespace before the Secret. The platform operator must
then add this block under the Agent's `spec`:

```yaml
tailscale:
  authKeySecretRef: tailscale-auth
```

On a full reconciliation, the operator copies the key to the gateway token
Secret and requests Tailscale Serve. The raw chart sets `tailscale.serve` to `false` by
default. For this integration, the operator changes it to `true`, unless
Agent Helm values override it.

A rotation of only a referenced Secret is not a guaranteed trigger. The
operator watches Agent and Team resources. Its steady-state cache hashes the
gateway Secret, not the source Tailscale Secret. For a Kubernetes-managed
rotation, create a namespace-local Secret with a different name. Then update
`authKeySecretRef` to that name so that the Agent spec changes. Do not print
keys to confirm the rotation.

If you remove the spec reference, a full reconciliation removes the copied
key from the gateway token Secret. But the removal itself does not revoke the
key or delete the original source Secret.

### Agent container and privileges

The node name comes from `tailscale.hostname` (default `cetirun`) and the Pod
ordinal. The Agent container starts `tailscaled`, `tailscale up`, and optionally
`tailscale serve` in the main container. These processes do not run in a
separate Tailscale sidecar. The Tailscale daemon state is on `emptyDir`, not on
the persistent home.

| Agent type | Default Serve target | Access boundary |
| --- | --- | --- |
| OpenClaw | `http://localhost:18789` | Gateway. Its configured `/ui/<agent-name>` base path still applies. |
| Hermes | `http://localhost:8642` | API server that uses `API_SERVER_KEY`, not the Webapp on 9120 |

Tailscale Serve publishes only the target in this table. The other listeners
of the Pod also accept connections on its tailnet address. Without ingress
mTLS, ttyd (port 7681), websockify (port 6080), and x11vnc (port 5900) listen
on all interfaces and do not require a credential. Limit the tailnet ACLs by
device and by port.

Do not expose the Hermes Webapp directly. Hermes 0.22.7 rejects an
unauthenticated non-loopback Webapp. CetiRun configures the Hermes built-in
password provider in addition to the platform access path.

The container uses kernel networking and creates `/dev/net/tun`. The chart adds
`NET_ADMIN` for Tailscale. Examine these privileges together with the
RuntimeClass and the network policy. CetiRun runs Tailscale Serve outside
OpenClaw. As a result, gateway token and device authentication are still
necessary on this access path. When Tailscale is enabled, the generated
OpenClaw configuration trusts only the exact `127.0.0.1` source of the Serve
proxy in the container.

Offline `helm template` cannot read a cluster Secret through Helm `lookup`.
A render that uses only `existingSecret` does not validate the Tailscale
enablement. Offline chart tests use a dummy `secrets.tailscaleAuthKey` to test
the enablement.

Sources: [portal Secret updates]({{< repo "app/cetirun-portal/internal/kube/client.go" >}}),
[operator reconciliation]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}),
[chart enablement]({{< repo "app/cetirun-chart/templates/_helpers.tpl" >}}),
[StatefulSet]({{< repo "app/cetirun-chart/templates/statefulset.yaml" >}}),
[OpenClaw startup]({{< repo "app/cetirun-chart/agents/openclaw/bootstrap/entrypoint.sh" >}}),
[Hermes startup]({{< repo "app/cetirun-chart/agents/hermes/bootstrap/hermes-entrypoint.sh" >}}),
and [desktop startup]({{< repo "app/cetirun-chart/configs/desktop-entrypoint.sh" >}}).
