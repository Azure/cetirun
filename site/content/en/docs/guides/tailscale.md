---
title: "Connect Tailscale"
description: "Give an instance a tailnet access path and keep the gateway authentication."
weight: 30
---

Tailscale is optional. Both runtime images have startup support for Tailscale.
You must have a tailnet, a local Tailscale client, an approved auth key, and a
deployment whose network and container privileges allow the integration. When
you add Tailscale, it does **not** remove the public Istio routes that exist.
It also does not repeat the portal's Entra and ownership checks on the tailnet
path.

## Prepare an auth key

Use your tailnet's administration console to create a key. The chart recommends
a reusable, ephemeral key because pods can be recreated.
Before you deploy, make sure that the key is reusable, because the first
connection can consume a single-use key. The Tailscale daemon state is on
`emptyDir`, not on the persistent home. As a result, it is possible that a
replacement pod must register again. Examine your tailnet ACLs, key expiry,
device approval, and HTTPS settings with the tailnet administrator.

Store the key privately. Do not put it into a committed manifest, shell history,
screenshots, or an issue.

## Portal path

The portal shows Tailscale controls only when the portal chart has
`features.tailscaleEnabled: true` (default `false`). When the flag is off, the
portal API also rejects key submissions that are not empty. The flag does not
disable Tailscale in instances that are already configured. It also does not
prevent use of the Kubernetes path.

1. Sign in. Then select **New Agent**, or edit an agent that exists.
2. Supply the auth key in the Tailscale field.
3. Submit the change. Then wait for the reconciliation.

The portal creates `tailscale-auth` in the instance namespace and records
`spec.tailscale.authKeySecretRef`. The overview can show or copy the key. The
key mask is a display feature, not an access-control boundary.

To update the portal-managed reference, enter a replacement key. If you leave
the edit field blank, the reference does not change. **Disable Tailscale**
removes the spec reference. Do not use this editor to rotate a differently
named Secret that was supplied through Kubernetes. The editor writes
`tailscale-auth`, but a custom reference that exists can stay unchanged.

## Kubernetes path

**Connected, mutating:** for an instance named `alice` that exists, put the key
in the protected local file `tailscale-auth-key`. Do not put a newline at the
end of the file. Then run these commands:

```bash
kubectl create secret generic tailscale-auth -n cetirun-alice \
  --from-file=TAILSCALE_AUTH_KEY=./tailscale-auth-key
kubectl patch agents.cetirun.microsoft.com alice --type=merge \
  -p '{"spec":{"tailscale":{"authKeySecretRef":"tailscale-auth"}}}'
```

For a new instance, an authorized platform operator must create its
`cetirun-<instance>` namespace before the Secret. The platform operator must
then add this block under the instance's `spec`:

```yaml
tailscale:
  authKeySecretRef: tailscale-auth
```

On a full reconciliation, the operator copies the key to the gateway Secret and
requests Tailscale Serve. The raw chart sets `tailscale.serve` to `false` by
default. For this integration, the operator changes it to `true`, unless
instance Helm values override it.

A rotation of only a referenced Secret is not a guaranteed trigger. The
operator watches instance and team resources. Its steady-state cache hashes the
gateway Secret, not the source Tailscale Secret. For a Kubernetes-managed
rotation, create a namespace-local Secret with a different name. Then update
`authKeySecretRef` to that name so that the instance spec changes. Do not print
keys to confirm the rotation.

## Connect

Sign your client in to the allowed tailnet. Then find the instance in the
Tailscale machines list. Its name comes from `tailscale.hostname` and the pod
ordinal. Usually, the name is `cetirun-0`. A name collision can add a suffix.
Always use the registered name:

```text
https://<node-name>.<tailnet>.ts.net
```

The runtime starts `tailscaled`, `tailscale up`, and optionally
`tailscale serve` in the main container. These processes do not run in a
separate Tailscale sidecar.

| Runtime | Default Serve target | Access boundary |
| --- | --- | --- |
| OpenClaw | `http://localhost:18789` | Gateway. Its configured `/ui/<instance>` base path still applies. |
| Hermes | `http://localhost:8642` | API server that uses `API_SERVER_KEY`, not the Webapp on 9120 |

As a result, the tailnet URL is not another copy of the portal, the browser
terminal, or the desktop. Do not expose the Hermes Webapp directly.
Hermes 0.22.7 rejects an unauthenticated non-loopback Webapp. CetiRun
configures the Hermes built-in password provider in addition to the platform
access path.

The container uses kernel networking and creates `/dev/net/tun`. The chart adds
`NET_ADMIN` for Tailscale. Examine these privileges together with the
RuntimeClass and the network policy. CetiRun runs Tailscale Serve outside
OpenClaw. As a result, Gateway token and device authentication are still
necessary on this access path. When Tailscale is enabled, the generated
OpenClaw configuration trusts only the exact `127.0.0.1` source of the Serve
proxy in the container.

## If it does not connect

Examine `TailscaleReady`, pod readiness, key expiry and reuse, device
authorization, tailnet ACLs, and network reachability. `TailscaleReady` means
that the controller prepared the key. It does not mean that a client connected
successfully.

Do not expose the gateway publicly to work around a tailnet issue. If a key is
disclosed, rotate it through the supported instance update path. Arrange the
key revocation with the tailnet administrator. If you remove the spec
reference, a full reconciliation removes the copied gateway key. But the
removal itself does not revoke the key or delete the original source Secret.

Sources: [portal Secret updates]({{< repo "app/cetirun-portal/internal/kube/client.go" >}}),
[operator reconciliation]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}),
[chart enablement]({{< repo "app/cetirun-chart/templates/_helpers.tpl" >}}),
[StatefulSet]({{< repo "app/cetirun-chart/templates/statefulset.yaml" >}}),
[OpenClaw startup]({{< repo "app/cetirun-chart/agents/openclaw/bootstrap/entrypoint.sh" >}}),
and [Hermes startup]({{< repo "app/cetirun-chart/agents/hermes/bootstrap/hermes-entrypoint.sh" >}}).

Offline `helm template` cannot read a cluster Secret through Helm `lookup`.
A render that uses only `existingSecret` does not validate the Tailscale
enablement. Offline chart tests use a dummy `secrets.tailscaleAuthKey` to test
the enablement.
