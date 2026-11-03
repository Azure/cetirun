---
title: "Your first instance"
description: "Create a persistent OpenClaw or Hermes workspace on a CetiRun deployment that already exists."
weight: 10
---

## Before you begin

You must have access to a configured CetiRun portal. You must also be a member
of the Entra ID group that the portal allows. Agents take their models from the
AI Gateway: the models that your administrators share, or the models of your
own GitHub Copilot subscription. Ask your administrator for the actual portal
URL. In these docs, `https://example.com/` is only a placeholder.

The portal's **Sign out** action uses `/oauth2/sign_out` on the portal origin.
As part of the deployment, we recommend that your administrator make sure that
sign-out clears the session. For the necessary routing, refer to
[Istio authentication](../../operations/istio/#prepare-identity-dns-and-tls).

## Create through the portal

1. Open the portal. Then sign in.
2. Select **New Agent**. If the deployment allows Hermes, Hermes is the default
   selection. To use OpenClaw, select OpenClaw explicitly. Both take their
   models from the AI Gateway.
3. Enter an optional instance-name suffix and display name. The owner alias
   comes from your authenticated identity, not from a field that you set. If
   you leave the name empty, the portal uses your alias as the name. For the
   synthetic owner `alice`, a suffix such as `example` gives `alice-example`.
   To let the platform generate the gateway token, leave the token empty.
4. Optionally, select a **Team**. A Team can select the policy and the Sandbox
   of the agent, and it can offer its own smart-routing models. To create the
   agent without a Team, keep **(none)**.
5. Select the models. The form shows **Use shared models** only when your
   administrators share models. It shows **Use GitHub Copilot** only when the
   deployment offers GitHub Copilot. The Policy of the selected Team can limit
   both. At least one source stays enabled. If the form says that no models are
   available, ask an administrator to examine model access.

   In **Default model (optional)**, select a model, or keep **(automatic)** for
   the deployment default. A Team's smart-routing models show
   `(smart routing)` after their names.
6. Select **Enable Microsoft Entra Agent Identity** only when the form shows it
   and delegated Azure access is necessary for you. Then select
   **Create Agent**. Wait until the platform provisions the instance. If the
   instance does not become ready, an administrator can examine the individual
   readiness conditions.
7. If you use GitHub Copilot, sign in to it. To sign in, open the instance.
   Then select **Sign in with GitHub** in the **Models** section of its
   overview. Authorization alone does not make the models ready: the Operator
   must prepare a Ready Copilot proxy and register its routes in the Gateway.
   Wait for the Copilot status, not only the overall Agent `Ready` condition.
   If it remains `ProxyStarting`, follow the
   [model-readiness checks](../../guides/lifecycle/#separate-provisioning-from-model-readiness)
   rather than repeating authorization or relying on a fixed wait time.
8. [Choose a model](../../guides/models/). Set the default model in the
   **Models** section. When you save it, the instance restarts.
9. To open the console of an OpenClaw instance, use the portal's access
   action. When the console asks for the gateway token, copy
   **Gateway Token** from the instance Overview. The first time that a browser
   connects to an instance, the console also asks to pair that browser. To pair
   the browser, open the instance's **Terminal** in the portal and run
   `openclaw devices list`. Approve the pending request of your browser with
   `openclaw devices approve <request-id>`, then reload the console. By
   default, `kubectl exec` cannot approve the request, because the exec
   restriction of the instance denies it.
10. If Agent Identity was enabled, use the instance overview to
    [connect Microsoft Entra](../../guides/agent-identity/). Do this before you
    ask the Agent to examine Azure resources.

The deployment controls the offered agent types, the visibility of Tailscale,
and the number of instances that each user can create. By default, the portal
chart allows one instance per user. This is a portal creation limit, not a
Kubernetes resource quota or a restriction on direct CR creation.

An instance of a Team with an Agent Policy runs in the Team's Sandbox.
Tailscale is not available in this Sandbox. Agent Identity is available only
when the Team's Policy allows it. Refer to
[Policy-bound Teams](../../operations/policy-teams/).

Sources: [creation form]({{< repo "app/cetirun-portal/web/src/components/CreateForm.tsx" >}})
and [portal instance handler]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}}).

## Create through Kubernetes

You must have permission to create cluster-scoped resources on a configured
cluster. Save this manifest as `instance.yaml` outside the repository.
Replace `alice-example` with the instance name and `spec.user.alias` with the
identity of its owner. When you apply the manifest, the instance and its
resources are created.

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Agent
metadata:
  name: alice-example
spec:
  team: example-team
  sandboxRef: standard
  agent:
    type: hermes
  modelAccess:
    sharedModels: true
  gatewayTokenSecretRef: gateway-token
  user:
    alias: alice
    displayName: Alice
```

Keep `metadata.name` and `spec.user.alias` stable. The instance namespace, Helm
release, and URL path come from the name. The ownership comes from the alias.
Do not use `ui`, because the controller reserves it.

`spec.team` and `spec.sandboxRef` select the policy and isolation settings that
an administrator defines. You must set `spec.agent`. If you do not set its
`type`, Hermes is the default. The effective type and runtime username cannot
change after creation.

An agent with `spec.sandboxRef` must request its model sources. Without
`modelAccess.sharedModels: true` or `modelAccess.githubCopilot: true`, it gets
no model. The Policy of the Team must also allow each source that the agent
requests.

Validate the manifest against the cluster's installed CRD. This command does
not save the manifest:

```bash
kubectl apply --dry-run=server -f instance.yaml
```

Server-side validation examines admission and the schema. It connects to the
cluster, but it does not test image pulls, scheduling, storage, routing, or
identity.

Create the instance:

```bash
kubectl apply -f instance.yaml
```

Examine the instance status:

```bash
kubectl get agents.cetirun.microsoft.com alice-example
kubectl wait --for=condition=Ready agents.cetirun.microsoft.com/alice-example --timeout=10m
kubectl wait --for=condition=IngressReady agents.cetirun.microsoft.com/alice-example --timeout=2m
kubectl get agents.cetirun.microsoft.com alice-example -o jsonpath='{.status.namespace}{"\n"}{.status.accessUrl}{"\n"}'
```

If `gateway-token` does not exist in `cetirun-alice-example`, the
[controller]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}})
creates it. The Secret contains `OPENCLAW_GATEWAY_TOKEN` and the separate
`CETIRUN_AUTH_KEY`. Hermes also must have `API_SERVER_KEY` and `hermes.env`,
and the controller supplies them. Do not put tokens in the CR or print the
Secret for diagnosis. If you supply a Secret that already exists, make sure
that its OpenClaw token is valid. The name of a Secret alone does not validate
its contents.

`spec.model` is optional. It names the default model of the agent, as the AI
Gateway offers it, for example `auto`. Without it, the operator supplies its
deployment default. For an agent of a policy-bound Team, the Policy must allow
the model. If it does not, the agent does not run. Its `PolicyResolved`
condition is then `False` with the reason `PolicyDenied`, and the message names
the model.

If `spec.helm.version` is empty, it is set to the
operator's runtime chart version at the first reconciliation. This pins the
instance chart. A change to only the operator's default chart version does not
upgrade the instances that already exist.

Open the URL in `status.accessUrl`. `Ready` reports workload readiness
independently of ingress. `IngressReady=True` confirms VirtualService
reconciliation. Examine TLS, sign-in, and model-provider access separately.

For Hermes, sign in to the native Dashboard with the username `cetirun` and the
Hermes API / Dashboard key. The instance Overview page shows this key. For
OpenClaw, enter the gateway token. Then approve the request to pair the browser
from the instance's Terminal, as in the portal steps above.

## Keep your workspace

The default storage settings keep the home directory across pod restarts.

> **Warning:** Before you delete an instance, back up important data. The
> deletion of an instance also deletes its namespace and the PVCs in that
> namespace. A StatefulSet `Retain` policy does not prevent namespace deletion.

For the suspension and deletion behavior, refer to
[instance lifecycle](../../guides/lifecycle/). Ask your administrator about the
backup and recovery procedures.
