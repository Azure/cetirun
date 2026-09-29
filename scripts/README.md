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
