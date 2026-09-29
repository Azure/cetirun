# CetiRun scripts

The [script reference](../site/content/en/docs/reference/scripts.md) covers
local checks, container builds, cloud deployment, and publication tools,
including the platform umbrella and independent Istio helpers.

For local chart validation, run from the repository root:

```bash
bash scripts/cetirun-helm-lint.sh
bash scripts/cetirun-helm-test.sh
```

Documentation validation is `make -C site check`. It does not run deployment,
publication, or end-to-end scripts.

## Pinned test tools

`ci/install_tools.py` is a separate, networked setup command, not part of the
offline validation targets. Its manifest pins Linux amd64/arm64 downloads by
SHA256. The `envtest` bundle installs Kubernetes 1.37 `kube-apiserver`/`kubectl`
and its bundled etcd into a new explicit directory. The installer verifies the
archive before reading only its named regular members, refuses existing outputs
and symlink destinations, and removes only files it created if writing fails.
It does not start the binaries or contact a cluster.

Downloads (including raw binaries) are capped at 128 MiB, each selected archive
member or raw binary at 128 MiB, and the total selected payloads across one
invocation at 384 MiB. Oversized advertised lengths fail before reading the
response; absent or understated lengths still use a bounded read. Selected
member sizes are checked before any member payload is read. Verified archives
and selected binaries remain in memory within these limits; streaming to disk
is not implemented.

Operator CI uses this same manifest and installer for both `envtest` and Helm 3,
with an explicit run/attempt-specific directory under `RUNNER_TEMP`. It sets
`KUBEBUILDER_ASSETS` to that binary directory and adds it to `PATH`, rather than
duplicating pins or relying on the runner's Helm version. Release provenance
for both Linux architectures is recorded in `ci/tools.json`.

See [envtest setup](../DEVELOPMENT.md#generated-files-and-dependencies) for the
separate provisioning and isolated API-server test commands.

## License headers

Run the same offline check used by CI:

```bash
make license-check
```

Checks include supported source files and Makefiles across the repository,
including `site/`, release, and E2E sources. Keep upstream and generated
exceptions aligned with [NOTICE](../NOTICE).

Follow the [header review and addition procedure](../site/content/en/docs/reference/scripts.md#license-headers)
before adding a notice. The tools do not replace existing notices, follow
symlinks, deploy resources, or access the network.
