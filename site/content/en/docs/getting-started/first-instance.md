---
title: "Your first instance"
description: "Create a persistent OpenClaw or Hermes workspace on an existing CetiRun deployment."
weight: 10
---

## Before you begin

You need access to a configured CetiRun portal, membership in its permitted Entra
ID group, and an account with the model provider you intend to use. Ask your
administrator for the actual portal URL; `https://example.com/` in these docs is
only a placeholder.

> **Sign-out limitation:** The portal sends **Sign out** requests to an
> `oauth2` subdomain, while the Istio chart serves `/oauth2` on the main domain.
> Ask your administrator to verify session logout before use. See
> [Istio authentication](../../operations/istio/#prepare-identity-dns-and-tls)
> for the routing requirements.

## Create through the portal

1. Open the portal and sign in.
2. Choose **New Agent**, then an agent type allowed by the deployment.
3. Enter an optional instance-name suffix and display name. The owner alias
   comes from your authenticated identity, not a field you choose. Leaving the
   name empty uses your alias; a suffix such as `example` produces
   `alice-example` for the synthetic owner `alice`. Leave the gateway token
   empty to let the platform generate it. For GitHub Copilot,
   leave the model at the deployment default or enter an authorized model ID.
   Live discovery requires an existing, authenticated instance.
4. Select **Create Agent** and wait for provisioning. An administrator can inspect
   individual readiness conditions if the instance does not become ready.
5. Use the portal’s access action to open the instance, then complete the
   provider login. For Copilot, use the portal’s device-login flow.
6. [Choose a model](../../guides/models/) from the live catalog after login.

The deployment controls which agent types are offered, whether Tailscale is
visible, and how many instances each user may create. The portal chart defaults
to one instance per user; this is a portal creation limit, not a Kubernetes
resource quota or a restriction on direct CR creation. Keep Teams disabled
for a first instance unless the administrator has configured its separate
identity and provisioning prerequisites.

Sources: [creation form]({{< repo "app/cetirun-portal/web/src/components/CreateForm.tsx" >}})
and [portal instance handler]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}}).

## Create through Kubernetes

You need permission to create cluster-scoped resources on a configured
cluster. Save the following manifest as `instance.yaml` outside the repository.
Replace `alice-example` with the instance name and `spec.user.alias` with its
owner's identity. Applying the manifest creates the instance and its resources.

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Agent
metadata:
  name: alice-example
spec:
  agentType: openclaw
  gatewayTokenSecretRef: gateway-token
  user:
    alias: alice
    displayName: Alice
```

Keep `metadata.name` and `spec.user.alias` stable. The former determines the
instance namespace, Helm release, and URL path; the latter determines
ownership. Do not use `ui`, which the controller reserves.

Validate the manifest against the cluster's installed CRD without saving it:

```bash
kubectl apply --dry-run=server -f instance.yaml
```

Server-side validation checks admission and the schema. It contacts the cluster
but does not test image pulls, scheduling, storage, routing, or identity.

Create the instance:

```bash
kubectl apply -f instance.yaml
```

Check the instance status:

```bash
kubectl get agents.cetirun.microsoft.com alice-example
kubectl wait --for=condition=Ready agents.cetirun.microsoft.com/alice-example --timeout=10m
kubectl wait --for=condition=IngressReady agents.cetirun.microsoft.com/alice-example --timeout=2m
kubectl get agents.cetirun.microsoft.com alice-example -o jsonpath='{.status.namespace}{"\n"}{.status.accessUrl}{"\n"}'
```

The [controller]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}})
creates `gateway-token` in `cetirun-alice-example` if absent, including
`OPENCLAW_GATEWAY_TOKEN` and the separate `CETIRUN_AUTH_KEY`. Hermes also needs
`API_SERVER_KEY` and `hermes.env`, which the controller supplies. Do not place
tokens in the CR or print the Secret for diagnosis. If supplying an existing
Secret, ensure its OpenClaw token is valid; merely naming a Secret does not
validate its contents.

`spec.model` is optional for Copilot because the operator supplies its deployment
default. `spec.helm.version`, if empty, is populated from the operator's runtime
chart version on first reconciliation. This pins the instance chart; changing
the operator's default chart version alone does not upgrade existing instances.

Open the URL in `status.accessUrl`. `Ready` reports workload readiness
independently of ingress. `IngressReady=True` confirms VirtualService
reconciliation; verify TLS, sign-in, and model-provider access separately.

## Keep your workspace

The default storage settings preserve the home directory across pod restarts.

> **Warning:** Deleting an instance deletes its namespace, including PVCs.
> A StatefulSet `Retain` policy does not prevent namespace deletion. Back up
> important data before deleting an instance.

See [instance lifecycle](../../guides/lifecycle/) for suspension and deletion
behavior, and ask your administrator about backup and recovery procedures.
