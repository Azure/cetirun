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
