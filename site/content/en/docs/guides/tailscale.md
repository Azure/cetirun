---
title: "Connect Tailscale"
description: "Give an instance a tailnet access path while preserving gateway authentication."
weight: 30
---

Tailscale is optional and has startup support in both runtime images. You need
a tailnet, a local Tailscale client, an approved auth key, and a deployment whose
network and container privileges permit the integration. Adding Tailscale does
**not** remove existing public Istio routes or reproduce the portal's Entra and
ownership checks on the tailnet path.

## Prepare an auth key

Use your tailnet's administration console to create a key. The chart recommends
a reusable, ephemeral key because pods can be recreated.
Verify reusability before deploying; a single-use key may be consumed on the
first connection. Tailscale daemon state is on `emptyDir`, not the persistent
home, so a replacement pod can need to register again. Review your tailnet
ACLs, key expiry, device approval, and HTTPS settings with its administrator.

Store the key privately. Do not put it into a committed manifest, shell history,
screenshots, or an issue.

## Portal path

The portal shows Tailscale controls only when the portal chart has
`features.tailscaleEnabled: true` (default `false`). Its API also rejects
non-empty key submissions while the flag is off. This does not disable
Tailscale in already configured instances or prohibit the Kubernetes path.

1. Sign in and choose **New Agent**, or edit an existing agent.
2. Supply the auth key in the Tailscale field.
3. Submit and wait for reconciliation.

The portal creates `tailscale-auth` in the instance namespace and records
`spec.tailscale.authKeySecretRef`. The overview can reveal or copy the key;
masking is a display feature, not an access-control boundary.
For the portal-managed reference, entering a replacement key updates it;
leaving the edit field blank keeps it unchanged. **Disable Tailscale** clears
the spec reference. Do not use this editor to rotate a differently named
Secret supplied through Kubernetes: it writes `tailscale-auth`, while an
existing custom reference can remain unchanged.

## Kubernetes path

**Connected, mutating:** for an existing instance named `alice`, place the key
in a protected local file `tailscale-auth-key` without a trailing newline, then:

```bash
kubectl create secret generic tailscale-auth -n cetirun-alice \
  --from-file=TAILSCALE_AUTH_KEY=./tailscale-auth-key
kubectl patch agents.cetirun.microsoft.com alice --type=merge \
  -p '{"spec":{"tailscale":{"authKeySecretRef":"tailscale-auth"}}}'
```

For a new instance, an authorized platform operator must create its
`cetirun-<instance>` namespace before the Secret, then include:

```yaml
tailscale:
  authKeySecretRef: tailscale-auth
```

under the instance's `spec`. On a full reconciliation, the operator copies the
key to the gateway Secret and requests Tailscale Serve. The raw chart defaults
`tailscale.serve` to `false`; the operator changes it to `true` for this
integration unless instance Helm values override it.

Rotating only a referenced Secret is not a guaranteed trigger: the operator
watches instance/team resources, and its steady-state cache hashes the gateway
Secret rather than the source Tailscale Secret. For a Kubernetes-managed
rotation, create a differently named namespace-local Secret and update
`authKeySecretRef` to that name so the instance spec changes. Do not print keys
to confirm rotation.

## Connect

Sign your client into the permitted tailnet and find the instance in the
Tailscale machines list. Its name is based on `tailscale.hostname` plus the pod
ordinal, commonly `cetirun-0`; collisions may add a suffix. Always use the name
actually registered:

```text
https://<node-name>.<tailnet>.ts.net
```

The runtime starts `tailscaled`, `tailscale up`, and optionally `tailscale serve`
inside the main container; it is not a separate Tailscale sidecar.

| Runtime | Default Serve target | Access boundary |
| --- | --- | --- |
| OpenClaw | `http://localhost:18789` | Gateway; its configured `/ui/<instance>` base path still applies |
| Hermes | `http://localhost:8642` | API server using `API_SERVER_KEY`, not the Dashboard on 9119 |

The tailnet URL is therefore not another copy of the portal, browser terminal,
or desktop. Do not expose the Hermes Dashboard directly: it is started with
`--insecure` and depends on the platform access path.

The container uses kernel networking, creates `/dev/net/tun`, and the chart
adds `NET_ADMIN` for Tailscale. Review those privileges alongside the
RuntimeClass and network policy.
Keep `tailscale.relaxGatewayAuth: false`. Enabling it can weaken gateway
authentication on non-tailnet access paths too; this setting is consumed by
the OpenClaw template, not by Hermes.

## If it does not connect

Check `TailscaleReady`, pod readiness, key expiry/reuse, device authorization,
tailnet ACLs, and network reachability. `TailscaleReady` means the controller
prepared the key, not that a client has successfully connected.

Do not expose the gateway publicly to work around a tailnet issue. Rotate a
disclosed key through the supported instance update path and arrange revocation
with the tailnet administrator. Removing the spec reference removes the copied
gateway key on full reconciliation, but does not itself revoke the key or
delete the original source Secret.

Sources: [portal Secret updates]({{< repo "app/cetirun-portal/internal/kube/client.go" >}}),
[operator reconciliation]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}),
[chart enablement]({{< repo "app/cetirun-chart/templates/_helpers.tpl" >}}),
[StatefulSet]({{< repo "app/cetirun-chart/templates/statefulset.yaml" >}}),
[OpenClaw startup]({{< repo "app/cetirun-chart/configs/entrypoint.sh" >}}),
and [Hermes startup]({{< repo "app/cetirun-chart/configs/hermes-entrypoint.sh" >}}).

Offline `helm template` cannot read a cluster Secret through Helm `lookup`.
A render using only `existingSecret` does not validate Tailscale enablement;
offline chart tests use a dummy `secrets.tailscaleAuthKey` to exercise it.
