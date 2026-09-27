# Pi Custom Agent

This example runs the real Pi coding agent through
`@earendil-works/pi-coding-agent`, pinned to **0.87.1** with an npm lockfile.
Upstream source: `earendil-works/pi`, tag `v0.87.1`,
`packages/coding-agent`. It uses Pi's read, bash, edit, write, grep, find and ls
tools rather than forwarding a prompt to a synthetic model.

The example uses Pi's native GitHub Copilot OAuth while CetiRun's AI Gateway
is unavailable. Credentials stay in this instance's private
`$HOME/.pi/agent/auth.json`. It does not import Hermes/OpenClaw credentials,
create a fake Virtual Key, or require `OPENAI_BASE_URL`/`OPENAI_API_KEY`.
The administrator must explicitly enable this image-owned authentication mode.
A missing required Gateway binding does not select it automatically.

## Runtime and isolation

The image declares `USER 1024:1024` and `HOME=/home/vibe`, matching the platform
Desktop identity. Pi works in `$HOME/workspace`; files and credentials survive
Pod replacement through the HOME PVC. The platform supplies the read-only
`$HOME/.agents` context and skills. Pi discovers those files through its SDK,
without loading arbitrary global extensions or installing skills at startup.

Set `spec.agent.port: 8080`. The example implements:

- `GET /healthz`: process readiness without a model request;
- `GET /v1/models`: Copilot models available to this instance;
- `POST /v1/chat/completions`: text messages, optional SSE streaming and Pi tools.

Requests run one at a time, with a two-minute deadline and an eight-tool-call
budget. Automatic retries and compaction are disabled. The HTTP adapter
reconstructs the supplied text history in an in-memory Pi session; it does not
implement a Chat UI or persistent HTTP conversation store.

The authentication service binds only `127.0.0.1:9099` in the Pod. It is not
included in the Service or public ingress. An administrator uses the bundled
CLI through an approved operations channel, as described below. Authentication
requests require a dedicated header and reject browser Origin headers and
foreign Host names. No token is returned through these APIs or written to
ordinary logs.
`PI_TELEMETRY=0` disables Pi's install telemetry.

Desktop, NetworkPolicy, exec restriction, Istio scope and storage ownership
remain platform-managed. ttyd stays disabled for Custom Agents.
This example does not provide a Portal login action or its own Chat UI.
Desktop access does not complete Copilot authentication.

## Build

Build the updated CetiRun base image first; it must contain the common
initializer's tools, including `cmp` from `diffutils`.

```bash
npm ci --ignore-scripts --no-audit --no-fund
npm test
npm run check
docker buildx build --platform linux/amd64 --load \
  --build-arg BASE_IMAGE=registry.example.com/cetirun-base:0.1.1 \
  --build-arg VERSION=0.1.1 --build-arg GIT_SHA=<source-sha> \
  -t registry.example.com/cetirun-pi-example:0.1.1 .
```

Run this from `examples/pi-agent`. Record the source commit, any uncommitted
patch digest, and the resulting image digest. Publishing the example is a
separate image operation; it is not added to the platform's six-image release.
Do not overwrite an existing release tag.

## Enable approved Custom provisioning

The Operator's `customRuntime` configuration is disabled by default. An
administrator enables it only for qualified image digests:

```yaml
cetirun-operator:
  customRuntime:
    enabled: true
    agentManagedModel: true
    approvedImages:
      - registry.example.com/cetirun-pi-example@sha256:<published-digest>
    storageClass: azureblob-nfs-premium
    storageSize: 20Gi
    accessMode: ReadWriteMany
    fsGroup: 1024
```

Keep existing platform values, identity and ingress configuration. The Operator
creates the dedicated namespace and HOME claim, waits for CSI binding, publishes
its configured canonical context, and records actual resource UIDs before
applying the Custom chart. Missing or foreign resources are not adopted by name.
The default context includes `cetirun-memory`; platform and Team shared skills
continue through the shared Skills path.

Provisioning requires a StorageClass that can bind before the consumer Pod is
created. A `WaitForFirstConsumer` class does not satisfy this provider's initial
binding gate. Qualify CSI permissions and mesh behavior separately.

During Pod-affecting updates, the provider persists an exclusive operation,
scales down the owned StatefulSet, and waits for HOME-using Pods to exit.
Completion is idempotent across Operator restarts. Unknown Helm outcomes retain
the completion outbox for explicit recovery; they are not retried as a new apply.
Image changes on an existing HOME require separate compatibility qualification.
Deleting an Agent follows the existing namespace/PVC deletion policy.

Create a **new** Custom Agent with an approved owner and the published digest.
Do not change a built-in Agent's type or manually provision its namespace/PVC:

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Agent
metadata:
  name: pi-example
spec:
  user:
    alias: example-user
    displayName: Example User
  agent:
    type: custom
    image: registry.example.com/cetirun-pi-example@sha256:<published-digest>
    username: vibe
    port: 8080
  helm:
    version: "0.1.1"
```

This template is not a ready-to-apply environment manifest: replace the owner,
digest and target context with approved values.

## Copilot authentication

Use an operations identity already approved for the target Agent. Confirm the
context, namespace and Pod before running the bundled CLI in the `cetirun`
container:

```bash
kubectl --context <approved-context> -n <agent-namespace> \
  exec <agent-pod> -c cetirun -- \
  node /opt/cetirun-pi/copilot-auth.mjs status
```

Ordinary exec access may be denied by platform policy. If no approved channel
is available, stop and ask the administrator; do not disable exec restrictions,
broaden permissions or expose port 9099 through a Service or ingress.

Pi's native login may enable otherwise unconfigured Copilot models on the
account. After approving that behavior, start login through the same channel:

```bash
kubectl --context <approved-context> -n <agent-namespace> \
  exec <agent-pod> -c cetirun -- \
  node /opt/cetirun-pi/copilot-auth.mjs login --allow-model-policy-updates
```

Complete the displayed GitHub device-code flow yourself using the approved
account. Never paste tokens into chat, Helm values or Kubernetes manifests.

Run the same CLI with `cancel` instead of `login --allow-model-policy-updates`
to cancel a pending device flow. The example does not expose an automatic
credential migration or logout/delete workflow.

If the cluster permits port-forwarding and its container runtime supports
forwarding to Pod loopback, the CLI can also run from this example directory:

```bash
kubectl --context <approved-context> -n <agent-namespace> \
  port-forward --address 127.0.0.1 pod/<agent-pod> 9099:9099 8080:8080
# In a second terminal:
node copilot-auth.mjs status
```

Verify `status` before using `login` over that connection. Do not assume this
route works for a sandboxed runtime such as Kata. `PI_AUTH_PORT` changes the
CLI's local destination port, not the authentication service's listening port.

## Validation

The Node tests use synthetic HTTP adapters except for the real SDK
context/Skills-discovery check. They do not perform OAuth or inference.
Live Copilot login, model-policy changes, model requests and Portal
interactions require separate validation; they are not established by these
tests or a Ready Agent.

For live validation, first check `/healthz`, the private auth status, and the
actual model IDs returned by `/v1/models`. After approving a model and request
budget, send one synthetic task. The following command runs inside the
`cetirun` container through the approved operations channel, or locally when a
working loopback port-forward to port 8080 has been established:

```bash
curl --fail --silent --show-error http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  --data '{"model":"<approved-copilot-model>","messages":[{"role":"user","content":"Read the cetirun-memory skill. Write a short synthetic validation note under memory, and report the exact file path. Do not read credentials or unrelated files."}]}'
```

Check actual tool execution and the resulting private file, not merely the
assistant's claim. Verify streaming and cancellation separately, then confirm
HOME and login persistence after an approved pause/resume or Pod replacement.
Keep logs and test messages private and synthetic. Readiness does not establish
successful Copilot authentication, tool execution, or CSI qualification.
