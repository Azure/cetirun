# VS Code Server with GitHub Copilot

Run Microsoft's official VS Code Server as a CetiRun Custom Agent, with a
browser editor, integrated terminal and native GitHub Copilot sign-in.
Workspace files, settings, extensions and server state persist on the HOME PVC.
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
    agentManagedModel: true
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

## Use

1. Wait for the Agent to become Ready, then select **Web UI** in the Portal.
2. Sign in to GitHub inside VS Code with the account holding your Copilot plan,
   completing organization SSO if required. No credentials are bundled or copied
   from other Agents, and Copilot does not use the platform AI Gateway.
3. Work in `$HOME/workspace`. Keep browser site data for login persistence;
   the PVC alone does not preserve the browser session.

The bootstrap preserves existing user settings and connects the read-only
`$HOME/.agents` instructions and Skills to Copilot. Check **Chat: Open
Customizations** for the selected harness; project instructions remain separate.
