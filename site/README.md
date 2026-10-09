<!-- Copyright (c) Microsoft Corporation. Licensed under the MIT License. -->

# CetiRun documentation site

Build and preview the CetiRun documentation with Hugo and Docsy. Component
READMEs link to the corresponding pages in this site.

```bash
make -C site check
make -C site serve
```

Run from the repository root. Preview: `http://localhost:1313/cetirun/`.
Override `HUGO=/absolute/path/to/hugo` to use an installed Hugo **0.164.0
extended**, and `PORT=1314` to select another loopback port. `make -C site help`
lists the entrypoints.

Prerequisites: Make, Go 1.27.2 (matching the repository's `.go-version`),
C/C++ build tools for Hugo extended, Node.js 24
with npm, and Python 3.11+. The first build downloads dependencies. `make -C
site test` runs the Python validator regression suite without network access.
`check` runs the repository header gate, builds, and checks links/fragments,
navigation, content/source contracts, search, the exact logo, and static
accessibility. A restored manifest's timestamp does not trigger another Hugo
installation when the existing binary already matches the pinned version.

Hugo extended 0.164.0 and Docsy 0.16.0 are pinned in the site sources.
Hugo cache stays outside the worktree at
`${XDG_CACHE_HOME:-$HOME/.cache}/cetirun/hugo`; override with `HUGO_CACHEDIR`.
The generated `public/`, `resources/`, `.bin/`, and `node_modules/` are ignored.
The logo is mounted directly from `../docs/logo.jpg`; there is no second copy.

## Content map

| Repository entry | Site content |
| --- | --- |
| Root README | `content/en/docs/overview/`, `getting-started/` |
| `docs/agent-harness-contract.md` | `content/en/docs/reference/agent-harness.md` |
| `docs/guides/msteams.md` | `content/en/docs/guides/msteams.md` |
| `docs/guides/tailscale.md` | `content/en/docs/guides/tailscale.md` |
| `docs/live-copilot-model-selection.design.md` | `content/en/docs/guides/models.md` |
| `app/cetirun-chart/README.md` | `content/en/docs/reference/helm.md` |
| `app/cetirun-operator/README.md` | `content/en/docs/reference/crds.md`, `helm.md` |
| `infra/README.md` | `content/en/docs/operations/infrastructure.md` |
| `infra/storage/README.md` | `content/en/docs/operations/storage.md` |
| `infra/istio/README.md` | `content/en/docs/operations/istio.md` |
| `scripts/README.md` | `content/en/docs/reference/scripts.md` |

Check documented fields, defaults, and commands against the corresponding
source whenever they change. Describe deployment prerequisites separately
from capabilities that are enabled by default, and do not treat a local
render as evidence of a working live deployment.

## Authoring and validation

The [contributor page](content/en/docs/contributing/site.md) explains the workflow.
Every source link made with the `repo` shortcode must exist in the checkout.
Documentation examples never run as part of the site build.
The checks also compare the site's Go minimum and documented Go version with
the repository's `.go-version`.

Every file under `docs/` must have an explicit destination in the checker's
`POINTERS` map or an asset role in `DOC_ASSETS`. Keep the source entry points
and their heading anchors working; root policies and component README links
are checked separately from rendered site URLs. The root Trademarks section
and its guideline link must be retained.

Keep future work on the single short [Roadmap](content/en/docs/overview/roadmap.md)
page, with `status: planned` and a visible `**Status: planned.**` label. Do not
include design contracts or executable setup examples there. Other pages
describe current behavior and its operational limitations.

`data/navigation.toml` assigns each documentation page to Overview, User Guide,
Operations, Reference, or Contributing. Add each page to exactly one module. The shared header, sidebar,
and breadcrumbs use that mapping; existing source paths and URLs need not move.
The validator checks every generated sidebar for missing or cross-module links.
Operations groups its tasks into Install, Configuration, and Troubleshooting.
Reference owns field and parameter definitions. GitHub is a separate external
link beside the five documentation modules.

After installing site dependencies, `make docs-check` at the repository root
delegates to the same site check. It is separate from `make check`, which covers
application and infrastructure validation. See
[local checks](content/en/docs/getting-started/local-checks.md) for the difference.

The default project subpath is `/cetirun/`. To test a different host/subpath:

```bash
make -C site check BASE_URL=https://docs.example.com/preview/
```

Keep PR validation read-only (`contents: read`). There is no Pages workflow
or deployment step; configuring a base URL does not enable publication.

## Upstream licenses

Dependency sources and notices are retained by the package managers:

- Docsy: Apache-2.0, `LICENSE` in the pinned Hugo module.
- Bootstrap: MIT, `node_modules/bootstrap/LICENSE`.
- jQuery: MIT, `node_modules/jquery/LICENSE.txt`.
- Lunr: MIT, `node_modules/lunr/LICENSE`.
- Font Awesome Free: icons CC BY 4.0, fonts SIL OFL 1.1, code MIT;
  `node_modules/@fortawesome/fontawesome-free/LICENSE.txt`.

No reference-site product content, generated pages, binaries, or caches are
vendored. Retain dependency notices in any distribution of bundled assets.
