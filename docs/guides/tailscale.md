# Tailscale VPN Setup for OpenClaw

Tailscale provides a mesh VPN that lets you access your OpenClaw instance directly from your local machine without exposing it through a public ingress.

## Prerequisites

- A [Tailscale account](https://login.tailscale.com)
- Tailscale client installed on your local machine ([download](https://tailscale.com/download))

## 1. Generate an Auth Key

1. Go to <https://login.tailscale.com/admin/settings/keys>
2. Click **Generate auth key**
3. Recommended settings:
   - **Reusable** -- pods may restart and re-authenticate
   - **Ephemeral** -- nodes are automatically removed when pods are deleted
4. Copy the key (format: `tskey-auth-kXXXX...`)

> **Important**: After generating, go back to the [keys page](https://login.tailscale.com/admin/settings/keys) and verify your key shows **Reusable**. A single-use key is consumed on first connect and automatically revoked -- any pod restart or redeployment will fail to authenticate.

## 2. Enable Tailscale

### Option A — Via Portal UI (Recommended)

1. Open the CetiRun Portal (`https://<your-domain>`) and sign in
2. On the **Create Instance** form, paste your Tailscale auth key into the **Tailscale Auth Key** field
3. Click **Create Instance**

The portal will automatically create the `tailscale-auth` Secret in the instance namespace and set the `spec.tailscale.authKeySecretRef` field on the CR. No `kubectl` commands needed.

Once the instance reaches **Ready**, the InstanceCard displays:

- **Tailscale Auth Key** — masked by default; click the eye icon to reveal

### Option B — Via kubectl

If you prefer manual deployment or need to add Tailscale to an existing instance:

```bash
kubectl create ns cetirun-<alias>

kubectl create secret generic tailscale-auth -n cetirun-<alias> \
  --from-literal=TAILSCALE_AUTH_KEY="tskey-auth-kXXXX..."
```

Agent CR:

```yaml
apiVersion: cetirun.microsoft.com/v1beta1
kind: Agent
metadata:
  name: <alias>
spec:
  gatewayTokenSecretRef: "gateway-token"
  tailscale:
    authKeySecretRef: "tailscale-auth"
  user:
    alias: <alias>
    displayName: "<Your Name>"
    team: <team>
    email: <email>
```

```bash
kubectl apply -f <cr-file>.yaml
```

## 3. Connect via Tailnet

1. Make sure your local machine is logged into the same Tailscale account
2. Find your tailnet name at <https://login.tailscale.com/admin/dns> (e.g. `tailxxxxx.ts.net`)
3. Verify the node `cetirun-0` appears in <https://login.tailscale.com/admin/machines>
4. Access the gateway via HTTPS:

```
https://<node-name>.<tailnet>.ts.net
```

The node name can be found in the [machines list](https://login.tailscale.com/admin/machines) or via `tailscale status`. The node name is typically `<tailscale.hostname>-<pod-ordinal>` (e.g. `cetirun-0`), but Tailscale may append a suffix (e.g. `cetirun-0-1`) if a previous node with the same name still exists. Always check the machines list for the actual name.
