---
title: Deploy an environment with GitHub Actions
description: Manually deploy a published MCR release to an existing AKS installation.
weight: 35
---

The **Deploy CetiRun environment** workflow consumes a published MCR release.
It does not build images, publish charts, or connect to the MCR release pipeline.
Use it to update an existing installation after its infrastructure, identities,
certificates, storage, and application credentials have been provisioned.

## Configure the GitHub Environment

Create a dedicated GitHub Environment for each deployment target and select it
in the workflow's **environment** dropdown. This keeps the target configuration
and credentials separate from older Environment settings. Creating a GitHub
Environment does not provision Azure infrastructure or install CetiRun; the
target must already be an installation managed by the three Helm releases below.
Configure these Environment secrets:

| Secret | Content |
| --- | --- |
| `E2E_KUBE_CONFIG` | A raw YAML or base64-encoded kubeconfig with embedded CA and client certificate/key or bearer token. |
| `DEPLOY_CONFIG_YAML` | The complete environment configuration YAML. |

The kubeconfig must select the context, cluster server, and cluster name in the
environment YAML. `target.clusterUID` is the UID of the target cluster's
`kube-system` namespace. External credential files, exec plugins, and disabled
TLS verification are not supported. The workflow does not log in to Azure or
obtain new AKS credentials. Rotate the Environment secret before its credentials
expire.

Start the environment YAML from the
[complete deployment example]({{< repo "app/cetirun-platform/ci/deployment-example.yaml" >}}).
Replace synthetic coordinates with the existing installation's reviewed inputs.
Omit `release.registry`, `release.version`, and `release.gitSHA`: the workflow
derives them from the release tag and public artifacts. If supplied, they must
match. `target.kubeconfig` is supplied by the job and need not be configured.

Keep the following configuration in the YAML:

- The three existing Helm releases, namespaces, domain, and managed Istio revision.
- Portal and Agent Identity configuration and existing credential Secret names.
- Gateway model routing, provider Secret references, and database persistence.
- Qualified Custom images, Agent HOME storage, and CetiAdmin settings.
- Marketplace repository and storage, and observability configuration.
- Existing admission, coordination, and native Sandbox configuration.

Application credentials stay in Kubernetes Secrets. Do not put provider keys,
OAuth secrets, cookie keys, database passwords, or TLS private keys in the YAML.
The pipeline validates dependencies; it does not register OAuth applications,
install cert-manager, or bootstrap Azure resources. For cert-manager certificates
already managed outside these Helm releases, retain the certificate reference
and use `istio.tlsMode: external`. Do not enable `publicTLS` bootstrap in this
workflow.

Review configuration changes against current live settings rather than copying
old computed Helm defaults. In particular, preserve model endpoints, storage
contracts, admission identities, and intentionally enabled features. A copied
old image pin is not evidence that it is compatible with a new release.

Upload the YAML from a local private file, without pasting it in Actions inputs:

```bash
gh secret set DEPLOY_CONFIG_YAML --env example-test < /private/environment.yaml
gh secret set E2E_KUBE_CONFIG --env example-test < /private/kubeconfig
```

Use environment deployment branch restrictions to limit access to the default
branch. The workflow also rejects dispatches from other branches. A GitHub
hosted Ubuntu runner must be able to reach the AKS API and the HTTPS Portal.
The workflow does not change network access rules when connectivity fails.

## Preview and deploy

Open **Actions → Deploy CetiRun environment → Run workflow** on the default
branch. Select the Environment and a release tag such as `v0.2.1`.

Leave **plan_only** enabled for the first run. The preview resolves images and
charts, validates the target, captures the existing baseline, renders the
candidate, and checks deployment prerequisites without writes. Its summary
contains release identities and change counts, not rendered manifests or secrets.

Once the preview passes, dispatch the same release with **plan_only** disabled.
The new run repeats all checks against current configuration and cluster state;
the earlier preview is not a reusable approval token. Configuration hashes let
you compare the two runs. A release tag is resolved once in each run. Deployment
uses the downloaded chart bytes and image digests, including the selected
Linux/AMD64 manifest identity. Do not overwrite release tags between preview and
deployment.

The workflow runs the deployment tooling from the dispatched default-branch
commit. A separate checkout of the release supplies metadata only; its scripts
are not executed. The artifact inventory includes all first-party images and
charts, including runtimes that are not immediately started. Missing artifacts
or inconsistent release metadata stop the run before cluster writes. Every image
must carry OCI source revision and version labels matching the selected release.
Missing labels also stop the run. Versions older than any existing Helm release
are rejected; recovery and rollback require a separate procedure.

The published Agent chart must already pin the OpenClaw, Hermes, Desktop,
Terminal, and Memory images to the resolved digests. A chart with empty runtime
digest defaults fails with `runtime_chart_digest_pins_missing`. The deployment
job does not repackage it or bypass its runtime profile qualification. This
requires a complete release whose publisher records image digests in the Agent
chart before publication.

The MCR publisher promotes the versioned images first, collects their digests,
then packages charts with `RELEASE_IMAGE_DIGESTS_FILE`. That JSON object maps
image artifact names to `sha256:` digests. The Agent chart embeds all five
runtime image pins, and its OpenClaw and Hermes profiles qualify those images.
This packaging order applies to releases produced after the publisher change;
it does not modify previously published releases. Publish a new complete release
instead of overwriting an older chart that lacks these pins.

Deployments to the same Environment are serialized without cancelling an active
deployment. GitHub concurrency may replace a pending run when another is queued;
it is not a queue that guarantees every requested deployment will execute.

## What changes

The deployment follows the existing coordinated order: CetiRun Istio and
authentication, the separate Broker release, and the platform. Existing admission
protection remains enabled. Each stage must succeed before the next begins.

The selected release supplies control-plane images and defaults for new Agents.
Existing Agent specifications, chart and image pins, governance resources, and
PVC identities are preserved. The pipeline rejects unplanned resource removals,
storage changes, and concurrent baseline changes.

Changing from a private registry to MCR can invalidate existing built-in Agent
image pins. `existing_agent_registry_conflict` is a preflight blocker, including
for suspended Agents. Resolve those Agents through a separately planned operation
before deploying; the pipeline does not migrate or delete them.

## Results and recovery

The run publishes only `report.json`, with a 30-day retention period. It records
the source and artifact identities, configuration hash, Helm revisions, stage
results, and zero completion submissions by the workflow. Provider background
request counts are not verified. Private kubeconfig, rendered values, snapshots,
and diagnostics are removed at the end of the job and are never uploaded.

On failure, read the reported stage and code. For a configuration or chart
failure whose details cannot safely be published, reproduce the read-only path
on an authorized workstation:

```bash
python3 -B scripts/deploy_environment.py \
  --source /private/release-checkout \
  --release-tag v0.2.1 \
  --config /private/environment.yaml \
  --kubeconfig /private/kubeconfig \
  --work /private/new-deployment-run \
  --report /private/deployment-report.json
```

Keep the new work directory private. It contains diagnostic details. The default
is read-only; `--apply` opts into deployment. The selected release checkout must
be clean, and the work directory must not already exist.

After a failed write, inspect the actual Helm histories and workloads before
attempting recovery. The workflow stops and does not retry mutations, perform
automatic rollback, or delete resources. Prior Helm revisions are recovery
references. They cannot guarantee recovery of CRDs, database migrations,
certificates, or external state.

A successful deployment verifies rollouts, image identities, HTTPS and the
authentication redirect, and preservation of existing Agent and storage state.
Preservation is observed for a stable 60-second window, with a five-minute
deadline. The same window checks control-plane rollout generations, replica
availability, and Pod readiness. This is bounded deployment evidence, not
indefinite monitoring.
It does not establish browser OAuth, successful native Agent chat, model inference,
Marketplace publication, or ADX ingestion. Run those product acceptance cases
separately.
