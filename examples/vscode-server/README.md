# VS Code Server with GitHub Copilot

Run Microsoft's official VS Code Server as a CetiRun Custom Agent, with a
browser editor, integrated terminal and native Copilot Chat using the platform
AI Gateway through Bring Your Own Key (BYOK). Chat and local Agent tool workflows
do not require GitHub sign-in or a Copilot plan.
Workspace files, installed extensions and server state persist on the HOME PVC;
browser-profile settings and BYOK secrets use VS Code's browser storage.
This example serves a Web UI, not the Custom Chat API.

Review the [VS Code Server license](https://code.visualstudio.com/license/server)
before use. Hosting, sharing and redistribution restrictions apply; a Pod per
user does not authorize a public hosted service. Image distribution requires
separate review. Copilot and extensions retain their own terms.

## Build

[vscode-lock.json](vscode-lock.json) pins official CLI and web-server artifacts
to **1.139.1**, with SHA-256 checksums for AMD64 and ARM64. The build uses the
CetiRun base image and requires explicit license acceptance. From the repository
root:

```bash
docker buildx build --platform linux/amd64 --load \
  --build-arg ACCEPT_VSCODE_SERVER_LICENSE_TERMS=true \
  -t cetirun-vscode:local examples/vscode-server
```

License acceptance is a build-time decision by the image builder. Users of the
approved prebuilt image do not need to pass this flag again; the image carries
the runtime acceptance setting.

For a custom `BASE_IMAGE`, provide Node.js 24, Git, Python with tarfile's `data`
extraction filter, and the CetiRun HOME initializer's tools. The example is not
included in platform image publishing. Deploy an approved image by digest.

## Deploy

Use matching Operator, CRD, runtime chart and Portal versions with Custom Web UI
support. An administrator enables the runtime and approves the image:

```yaml
cetirun-operator:
  customRuntime:
    enabled: true
    approvedImages:
      - registry.example.com/cetirun-vscode@sha256:<approved-digest>
    storageClass: <qualified-storage-class>
    storageSize: 20Gi
    accessMode: ReadWriteOnce
    fsGroup: 1024
```

Replace the example owner, image digest and chart version in
[agent.yaml](agent.yaml), then apply it to your approved cluster:

```bash
kubectl --context <cluster-context> apply -f examples/vscode-server/agent.yaml
```

The manifest sets `webUI: true` and `port: 8080`, publishing the editor at
`/ui/vscode-example/`. Platform Desktop and Terminal are disabled because VS Code
provides its own terminal. The image runs as UID/GID 1024 with `HOME=/home/vibe`;
let the platform provision its namespace and storage.

Every approved Custom image receives the Gateway binding. The Operator injects
`OPENAI_BASE_URL` and the instance's Secret-backed
`OPENAI_API_KEY`. The manifest enables shared models and disables the optional
Gateway Copilot upstream.

## Use

1. Wait for the Agent to become Ready, then select **Web UI** in the Portal.
2. Trust your workspace when VS Code asks. The bundled **CetiRun AI Gateway**
   configuration extension then discovers the models allowed by the instance's
   Virtual Key and adds a native **Custom Endpoint** group. Setup enables the
   bundled Copilot extension without signing in or enrolling in a Copilot plan.
   Workspace trust and tool approval prompts still apply.
3. Open Chat and select a model under **CetiRun AI Gateway** if you already have
   a saved model preference. New profiles default to the first model in the
   sorted catalog; utility tasks also use a Gateway model. Use the local Chat
   Agent workflow, not a cloud-hosted coding agent.
4. Work in `$HOME/workspace`. Keep browser site data: BYOK configuration and
   Secret Storage belong to the active VS Code browser profile, not only the PVC.

The bootstrap preserves existing user settings and connects the read-only
`$HOME/.agents` instructions and Skills to Copilot. Check **Chat: Open
Customizations** for the selected harness; project instructions remain separate.

## Gateway configuration and limits

The extension uses the pinned VS Code command `lm.migrateLanguageModelsProviderGroup`
to store the Virtual Key in native Secret Storage. `chatLanguageModels.json`
contains an `${input:chat.lm.secret...}` reference, not a plaintext key. The
extension does not keep the key in its own state. Protect browser profile data:
native Secret Storage is not an isolation boundary from the workspace owner.
An explicit `chat.aiDisabled` or legacy `chat.disableAIFeatures` setting stops
automatic configuration without re-enabling AI features.

Discovery uses `GET /v1/models`; Copilot sends model requests directly to the
Gateway's `POST /v1/chat/completions`. The configuration extension does not proxy
chat traffic. Discovery has a timeout and catalog-size limit, rejects redirects,
and filters unresolved Gateway access-group names out of the model list.

The demo targets models with streaming Chat Completions, tool calling and at
least a 128K context window. It configures text input with a 114,688-token input
budget and a 16,384-token output limit. These are demo defaults, not discovered
model capabilities; adjust native configuration for models with different
limits or capabilities. Sampling options `temperature` and `top_p` are set to
`null`, which tells the pinned
Copilot implementation to omit its defaults and use the model server's defaults.

On startup, or with **CetiRun: Configure AI Gateway Models**, the extension
refreshes its untouched model group and defaults when the catalog or injected
key changes. A Secret-backed environment variable changes only after the
container is restarted. The extension preserves other model groups, comments,
and explicit user settings. If you edit the Gateway group yourself, it becomes
user-managed and automatic refresh stops. To return to managed configuration,
remove that group in **Chat: Manage Language Models**, then run the CetiRun
configuration command. Save dirty model configuration before doing so.

Empty catalogs, denied requests and invalid configuration report a setup warning;
they do not enable a GitHub login fallback. Partial Gateway environment bindings
fail startup. The legacy `agentManagedModel` value is accepted during an
upgrade and ignored so the Operator can stop and migrate old workloads; new
platform model access always uses the Gateway. Existing browser
credentials/configuration are not deleted when switching modes; use a separate
profile for a different mode.

BYOK covers Chat, local Agent workflows and utility tasks. It does **not** supply
GitHub's inline code completions, semantic search or embedding-dependent features.
Those features still require the applicable GitHub account/service access.
Organization BYOK policy applies if the user signs into a managed Copilot account.
Experimental Agent Host sessions are not configured by this demo.

References: [VS Code language models and Custom Endpoint configuration](https://code.visualstudio.com/docs/agent-customization/language-models),
[BYOK without GitHub sign-in](https://code.visualstudio.com/blogs/2026/06/18/byok-vscode),
and the pinned [native configuration command](https://github.com/microsoft/vscode/blob/04c0d99f4fb0d8afe6ce4f0c58e31e183ac3e4b1/src/vs/workbench/contrib/chat/browser/actions/chatLanguageModelActions.ts).
Recheck the internal command and configuration schema when updating the VS Code lock.

## Offline validation

From the repository root:

```bash
node --test examples/vscode-server/test/*.test.mjs
python3 -B -m unittest discover -s scripts/tests -p test_vscode_example.py -v
```

The tests use loopback servers, synthetic credentials and a simulated VS Code API.
They cover Gateway discovery, native configuration handoff, secret references,
restart/key refresh, user-file preservation and the existing Web IDE proxy.
They do not start VS Code or validate a real Copilot request. Separately qualify
an approved image with a signed-out browser profile, a native Agent tool task
and matching Gateway request metadata. Recheck after restart and key rotation.
Deployment and live model calls require separate authorization.
