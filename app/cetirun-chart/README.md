# CetiRun agent chart

The chart deploys an OpenClaw or Hermes instance. Read the
[Helm configuration reference](../../site/content/en/docs/reference/helm.md)
for settings and defaults, and the
[local checks guide](../../site/content/en/docs/getting-started/local-checks.md)
for credential-free rendering.
Maintainers updating the bundled Hermes release should also follow
[the Hermes runtime upgrade procedure](agents/hermes/README.md).

`values.yaml`, `values.schema.json`, and `templates/` are the source of truth.
Operator-resolved values differ from standalone chart defaults. A local render
does not establish storage, RuntimeClass, image access, or cloud readiness.

From the repository root:

```bash
bash scripts/cetirun-helm-lint.sh
bash scripts/cetirun-helm-test.sh
make -C site serve
```

See [package registry overrides](../../site/content/en/docs/reference/scripts.md#container-build-helpers)
for public npm/PyPI defaults and alternate mirrors, and
[configuration and skills](../../site/content/en/docs/concepts/configuration.md)
for runtime package and Git settings.
