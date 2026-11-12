<!-- Copyright (c) Microsoft Corporation. Licensed under the MIT License. -->

# CetiRun documentation site

Build and preview the CetiRun documentation with Hugo and Docsy. Component
READMEs link to the applicable pages in this site.

```bash
make -C site check
make -C site serve
```

Run these commands from the repository root. The preview URL is
`http://localhost:1313/cetirun/`. To use an installed Hugo **0.164.0 extended**,
override `HUGO=/absolute/path/to/hugo`. To select another loopback port,
override `PORT=1314`. `make -C site help` shows the entrypoints.

Prerequisites: Make, Go 1.27.4 (the same version as the repository's
`.go-version`), C/C++ build tools for Hugo extended, Node.js 24 with npm, and
Python 3.11+. The first build downloads dependencies. `make -C site test` runs
the regression suite of the Python validator without network access. `check`
runs the repository header gate and builds the site. It also examines links and
fragments, navigation, content and source contracts, search, the exact logo,
and static accessibility. If the installed Hugo binary is already the pinned
version, the timestamp of a restored manifest does not cause another Hugo
installation.

The site sources pin Hugo extended 0.164.0 and Docsy 0.16.0. The Hugo cache
stays outside the worktree at `${XDG_CACHE_HOME:-$HOME/.cache}/cetirun/hugo`.
You can override this location with `HUGO_CACHEDIR`. The generated `public/`,
`resources/`, `.bin/`, and `node_modules/` are ignored. The logo is mounted
directly from `../docs/logo.jpg`. There is no second copy.

## Content map

| Repository entry | Site content |
| --- | --- |
| Root README | `content/en/docs/overview/`, `getting-started/` |
| `docs/agent-harness-contract.md` | `content/en/docs/reference/agent-harness.md` |
| `docs/guides/tailscale.md` | `content/en/docs/guides/tailscale.md` |
| `docs/live-copilot-model-selection.design.md` | `content/en/docs/guides/models.md` |
| `app/cetirun-chart/README.md` | `content/en/docs/reference/helm.md` |
| `app/cetirun-operator/README.md` | `content/en/docs/reference/crds.md`, `helm.md` |
| `infra/README.md` | `content/en/docs/operations/infrastructure.md` |
| `infra/storage/README.md` | `content/en/docs/operations/storage.md` |
| `infra/istio/README.md` | `content/en/docs/operations/istio.md` |
| `scripts/README.md` | `content/en/docs/reference/scripts.md` |

Compare the documented fields, defaults, and commands with the applicable
source each time that they change. Describe deployment prerequisites separately
from the capabilities that are enabled by default. Do not use a local render as
evidence that a live deployment works.

## Authoring and validation

The [contributor page](content/en/docs/contributing/site.md) describes the workflow.
Every source link from the `repo` shortcode must exist in the checkout.
Documentation examples never run as part of the site build.
The checks also compare the Go minimum of the site and the documented Go version
with the repository's `.go-version`.

Every file under `docs/` must have an explicit destination in the checker's
`POINTERS` map or an asset role in `DOC_ASSETS`. Make sure that the source
entry points and their heading anchors continue to work. Root policies and
component README links are examined separately from rendered site URLs. You
must keep the root Trademarks section and its guideline link.

Keep future work on the single short [Roadmap](content/en/docs/overview/roadmap.md)
page, with `status: planned` and a visible `**Status: planned.**` label. Do not
include design contracts or executable setup examples there. Other pages
describe the current behavior and its operational limits.

`data/navigation.toml` assigns each documentation page to Overview, User Guide,
Operations, Reference, or Contributing. Add each page to exactly one module.
The shared header, sidebar, and breadcrumbs use that mapping. It is not
necessary to move the current source paths and URLs. The validator examines
every generated sidebar for links that are not there and for cross-module links.

Operations groups its tasks into Install, Configuration, and Troubleshooting.
Reference owns field and parameter definitions. GitHub is a separate external
link beside the five documentation modules.

After you install the site dependencies, `make docs-check` at the repository
root runs the same site check. It is separate from `make check`, which covers
application and infrastructure validation. For the difference, refer to
[local checks](content/en/docs/getting-started/local-checks.md).

The default project subpath is `/cetirun/`. To test a different host or
subpath, run:

```bash
make -C site check BASE_URL=https://docs.example.com/preview/
```

Keep PR validation read-only (`contents: read`). There is no Pages workflow
or deployment step. A configured base URL does not enable publication.

## Upstream licenses

The package managers keep the dependency sources and notices:

- Docsy: Apache-2.0, `LICENSE` in the pinned Hugo module.
- Bootstrap: MIT, `node_modules/bootstrap/LICENSE`.
- jQuery: MIT, `node_modules/jquery/LICENSE.txt`.
- Lunr: MIT, `node_modules/lunr/LICENSE`.
- Font Awesome Free: icons CC BY 4.0, fonts SIL OFL 1.1, code MIT,
  `node_modules/@fortawesome/fontawesome-free/LICENSE.txt`.

No reference-site product content, generated pages, binaries, or caches are
vendored. Keep dependency notices in any distribution of bundled assets.
