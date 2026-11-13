---
title: "Contribute to documentation"
linkTitle: "Documentation"
description: "Write, preview, and validate CetiRun documentation."
weight: 10
---

## Toolchain

The site pins its tools and dependencies in these files:

- Hugo extended: `HUGO_VERSION` in [`site/Makefile`]({{< repo "site/Makefile" >}})
- Docsy: [`site/go.mod`]({{< repo "site/go.mod" >}})
- Bootstrap, Font Awesome, jQuery, and Lunr:
  [`site/package.json`]({{< repo "site/package.json" >}}) and its lock file

Use these tools:

- Go 1.27.4 to install the pinned Hugo binary. This version matches the
  repository's [`.go-version`]({{< repo ".go-version" >}}).
- A C/C++ compiler for the extended Hugo build
- Node.js 24 with npm
- Python 3.11 or later
- Make

When you change the toolchain, refer to the official
[Hugo installation instructions](https://gohugo.io/installation/) and the
[Docsy module guide](https://www.docsy.dev/docs/get-started/docsy-as-module/).

## Check and preview

Run these commands from the repository root. Install the dependencies one
time. This step needs network access. It builds Hugo extended, downloads the
Hugo modules and the npm packages, and builds the site:

```bash
make -C site build
```

`make -C site deps` installs only the Hugo binary and the npm packages. It does
not download the Hugo modules, so it is not sufficient for the offline gate.

After each edit, run the documentation gate, which runs offline, and preview
the site:

```bash
make docs-check
make -C site serve
```

Open `http://localhost:1313/cetirun/`. By default, the server binds to
loopback. If that port is not available, add `PORT=1314`. If a Hugo extended
binary of the pinned version is already installed, add
`HUGO=/absolute/path/to/hugo` to each command. `make -C site test` runs only the
validator tests. `make check` does not include documentation.

`make docs-check` runs `make -C site check` with Go toolchain and module
downloads and npm network access disabled. Keep `module.proxy = ""` in
`hugo.toml`. With this value, Hugo inherits the `GOPROXY=off` setting of the
gate and does not replace it with its default `direct` mode.

`check` runs the validator tests and the repository-wide header check. It
builds the site and then validates these items:

- Internal URLs and fragments
- Repository Markdown entry points and heading anchors
- Navigation
- The `docs/` inventory
- Search index coverage
- Go-version consistency
- Logo integrity
- Basic accessibility

It does not run the deployment examples. Static checks do not replace
keyboard, screen-reader, mobile, or browser visual review.

To test a different host or subpath, add a base URL, for example
`make -C site check BASE_URL=https://docs.example.com/preview/`. The default
project subpath is `/cetirun/`.

By default, the Hugo cache is in `$XDG_CACHE_HOME/cetirun/hugo` or
`$HOME/.cache/cetirun/hugo`, outside the worktree. To use a different
location, set `HUGO_CACHEDIR`. `.bin`, `node_modules`, `public`, and
`resources` are ignored build products. Do not commit them.

## Edit content

Add Markdown under `site/content/en/docs` with a title, a description, and a
numeric weight. The `weight` orders the child pages on a section landing page.
It does not set the sidebar order. Use a section `_index.md` to give a reader
an entry point. Keep relative links valid from the rendered URL, with its
trailing slash.

Use the `repo` shortcode for links to source paths. The validator makes sure
that the target exists in the checkout. Keep descriptions specific enough to
work in navigation and search.

When you add or move a file under `docs/`, update the checker's `POINTERS` map
or `DOC_ASSETS` classification. When you replace a document with a pointer,
keep the inbound repository links and their heading anchors.

Keep future work on one short Roadmap page. Its front matter has
`status: planned`, and the page shows a visible `**Status: planned.**` label.
Do not add detailed plans, design contracts, or executable setup examples to
it. Do not add unimplemented usage instructions to other documentation. Keep
current usage and its limitations accurate until the implementation changes.

Keep the Trademarks section of the root README and its guideline link. The
checks require them.

Assign every page to one module in `site/data/navigation.toml`: Overview,
User Guide, Operations, Reference, or Contributing. The sidebar shows the pages
in the order of the `pages` list in that file. Navigation is scoped to that
module on both the landing page and its child pages. Source paths can stay
unchanged. The checks reject pages without an assignment, duplicates, and
cross-module sidebar entries.

Put user tasks under User Guide, in the Get Started, Using Agents, or
Integrations group. Operations groups platform tasks into Install,
Configuration, and Troubleshooting. Add a page to the group that matches its
task. Link to Reference for field and parameter definitions. Do not repeat
these definitions.

### Writing style

Write all English documentation in ASD-STE100 Simplified Technical English
(Issue 9). The most important rules are these:

- Write one topic in each sentence. Keep procedural sentences to at most 20
  words and descriptive sentences to at most 25 words.
- Write one topic in each paragraph, with at most six sentences.
- In procedures, use the imperative and one action in each step. Put a
  condition before the action: "If ..., do ...".
- Use the active voice and the simple present tense.
- Use simple, common words, with one meaning for each word. Use one name for
  each item on all pages.
- Do not use phrasal verbs. For example, write "configure" or "install", not
  "set up".
- Keep the articles. Do not use contractions. Use noun clusters of at most
  three words.
- Use lists and tables for complex content.

Also follow these project rules:

- After a limitation, write what the reader can do or link to it. For example,
  write "The flag only hides the Tailscale controls. To disable Tailscale for
  one Agent, use **Disable Tailscale** in its editor." Do not write only "The
  flag does not disable Tailscale."
- Use "examine" only for an action of a person. For a tool or a command, use
  "check", "validate", or "render". For example, write "A local render checks
  only the templates", not "A local render does not examine Azure".
- Name concepts and reference pages with short noun phrases, such as
  **Architecture** and **Security**. Name tasks with actions, such as
  **Choose a model** or **Manage an Agent**.
- Write titles, headings, and navigation labels in sentence case. Capitalize
  only the first word, the first word after a colon, and proper names, for
  example "Choose a model" or "Models through the AI Gateway". The
  [Glossary](../../concepts/glossary/) lists the product names.
- Describe what the product does. Do not narrate development work or compare
  the product with an unfinished design.
- Start with the reader's task or the component's purpose. Avoid introductions
  that only announce what the page describes.
- Explain prerequisites and limitations where the reader needs them. Use a
  warning for a security or data-loss risk, and put it before the step that it
  applies to. In the warning, state the consequence and the action to take.
- Keep API fields, commands, defaults, and identifiers accurate. An edit to the
  prose must not change their meaning or imply that a defect is fixed.
- When a compatibility difference is important, use a specific version.

Use the [Kubernetes documentation style guide](https://kubernetes.io/docs/contribute/style/style-guide/)
for technical text. Use Google's
[timeless documentation guidance](https://developers.google.com/style/timeless-documentation)
for release-independent text.

Distinguish local rendering, connected read-only inspection, and cloud changes.
Examples must use placeholders and must not invent a release registry,
supported feature, or successful integration test.

The original `docs/logo.jpg` is mounted into the generated site. Do not resize,
re-encode, or replace that source image.

### Style checks

`make -C site check` checks the writing style of each page after the build.
`make docs-check` runs the same check. Each style rule has one of three
levels:

- An `error` rule fails the check, for example a contraction or a link text
  such as "here".
- A `pending` rule only reports, because some pages do not meet it yet.
- A `review` rule only reports. It marks text for a person to judge.

To list the findings of each page and the number of findings of each rule,
run this command:

```bash
python3 site/scripts/check_site.py --style-report
```

The last line of the report names the pending rules that have no findings.
Change the level of these rules to `error` in `STYLE_RULES` of
[`check_site.py`]({{< repo "site/scripts/check_site.py" >}}). The rules ignore
code, link targets, and bold UI labels. The `heading-case` rule keeps the
capital letters of the names in `PROPER_NAMES`. When you add a product name to
the glossary, also add it to this list. The rule also compares each navigation
group in `site/data/navigation.toml` with the title of its page.

The `glossary-name` rule fails on the names that the
[Glossary](../../concepts/glossary/#other-names) replaces, for example
"runtime chart" or "the Gateway". To mention such a name on purpose, put it in
quotation marks.

## Publication

The base URL is configured for a possible `/cetirun/` project site, so that
local validation finds subpath mistakes. The repository provides pull-request
validation and local preview only. It does not publish the site. There is no
Pages deployment workflow, deployment permission, or enabled publication step.

The [`CetiRun docs checks` workflow]({{< repo ".github/workflows/cetirun-docs-validation.yml" >}})
uses the repository Go version and the dependency locks of the site. Keep it
read-only, with `contents: read` permissions. Its `paths` filter lists the
changes that start it. The filter does not include `examples/`, `AGENTS.md`, or
`CODE_OF_CONDUCT.md`. But the site links to files in `examples/` and to
`CODE_OF_CONDUCT.md`, and the checks validate the links in `AGENTS.md`. If you
change, move, or remove one of these files, run `make docs-check` locally.

The workflow does not make its check a repository-required gate. That is a
maintainer setting outside the site sources.

## Theme and third-party assets

Docsy supplies the documentation sidebar, table of contents, breadcrumbs,
theme selection, and client-side search. CetiRun supplies its own home layout,
styles, and local-asset head. jQuery and Lunr are bundled from pinned npm
dependencies. The site configures no analytics service.

The [page head]({{< repo "site/layouts/_partials/head.html" >}}) reads the
`td-color-theme` value from the browser `localStorage`. It uses this value to
apply the theme that the reader selected.

Keep upstream license notices with dependencies. For dependency license
locations and the content map, refer to
[the site source guide]({{< repo "site/README.md" >}}). These statements are
about the documentation site, not about telemetry in the CetiRun runtime or how
the runtime handles data. They also do not cover the request logs of a hosting
provider or the external sites that links open. Review the hosting behavior
separately.
