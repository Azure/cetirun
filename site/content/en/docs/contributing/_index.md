---
title: "Contributing"
description: "Find the relevant source, propose a focused change, and validate it locally."
weight: 80
no_list: true
---

Contribute code, tests, documentation, bug reports, or feedback. Start with
[CONTRIBUTING.md]({{< repo "CONTRIBUTING.md" >}}) and the
[Code of Conduct]({{< repo "CODE_OF_CONDUCT.md" >}}) for contribution requirements.

- [Development](development/) — source layout and local setup
- [Testing](../getting-started/local-checks/) — component and documentation checks
- [Documentation](site/) — write, preview, and validate the site

Search existing issues before filing one. Discuss changes larger than a typo
before implementing them. If maintainers have already agreed on the work, link
that discussion instead of reopening the decision. For questions and
non-security bug reports, follow [SUPPORT.md]({{< repo "SUPPORT.md" >}}).
Community support is best-effort, with no promised response time, release
schedule, long-term support window, or backport. A repository issue is not an
Azure support ticket or a commercial support agreement.

## Find the component

| Change | Source |
| --- | --- |
| Instance reconciliation or API | [Operator]({{< repo "app/cetirun-operator" "tree" >}}) |
| Agent packaging or Helm settings | [Runtime chart]({{< repo "app/cetirun-chart" "tree" >}}) |
| Portal backend or frontend | [Portal]({{< repo "app/cetirun-portal" "tree" >}}) |
| Shared infrastructure | [Infrastructure]({{< repo "infra" "tree" >}}) |
| Reader documentation | [Site content]({{< repo "site/content/en/docs" "tree" >}}) |

Use [DEVELOPMENT.md]({{< repo "DEVELOPMENT.md" >}}) for local setup and validation.
For documentation, follow the [site guide](site/).
[GOVERNANCE.md]({{< repo "GOVERNANCE.md" >}}) describes project responsibilities.
The component map does not assign owners or establish automatic code-owner
review routing; ask maintainers to arrange review for the affected area.

## Make a focused change

1. Use a meaningful branch, such as `docs/local-development`, and keep the change
   focused. Preserve unrelated work and coordinate edits across components.
2. Explain the problem and compatibility or operational effects. Add a
   regression test where practical and update documentation for changed
   behavior. Regenerate checked-in artifacts from source using the guidance in
   [DEVELOPMENT.md]({{< repo "DEVELOPMENT.md" >}}).
3. Run checks for the affected components. Record exact commands, results,
   missing dependencies, and checks not run. Dependency installation may need
   network access; local checks do not authorize deployment, release, or live
   integration work.
4. Inspect the full diff and its evidence for unintended files and sensitive
   data. Open a pull request against `main` using the repository template and
   link the agreed issue or proposal.

Run local tests before submitting a change. Cloud deployment, model-provider
access, and Tailscale integration need separate tests in an authorized
environment; security review remains separate from test results.
Contributors do not need cloud credentials or publishing access.

## Keep reports and examples safe to publish

Use synthetic data, public example domains such as `example.com`, and dummy
values such as `sk-dummy`. Include the affected component, commit or artifact
version, expected and actual behavior, and a minimal shareable reproduction.
Review screenshots as well as text.

Do not publish credentials, cookies, authorization headers, kubeconfigs, private
endpoints or registries, tenant or subscription identifiers, customer data,
private agent conversations, or internal documents and logs. Share only a short,
sanitized excerpt appropriate to the issue, not a full diagnostic bundle.
The [runtime privacy disclosure]({{< repo "PRIVACY.md" >}}) explains why session
logs, browser state, and portal responses can contain sensitive information.

Report suspected vulnerabilities privately through the Microsoft Security
Response Center (MSRC), following
[SECURITY.md]({{< repo "SECURITY.md" >}}). Public issues and pull requests are
not vulnerability-reporting channels, even for sanitized reproductions.
Use the [Code of Conduct]({{< repo "CODE_OF_CONDUCT.md" >}}) contact information
for conduct concerns.

## CLA and independent review

Most contributions require a Microsoft Contributor License Agreement (CLA).
Follow the CLA bot's instructions and the terms linked from
[CONTRIBUTING.md]({{< repo "CONTRIBUTING.md" >}}). If the check is absent or
unclear, ask a maintainer; a missing check is not an exemption.

Before merge, a maintainer other than the author must approve, applicable CLA
requirements and required checks must be satisfied, and review concerns must be
resolved. Authentication, isolation, CRDs, storage, and shared contracts may
need additional reviewers. Maintainers verify these requirements, including
those not enforced by repository settings, and decide whether and when to
accept and release a contribution.

## Licenses and third-party material

First-party CetiRun material is licensed under [LICENSE]({{< repo "LICENSE" >}})
(MIT). Preserve existing copyright and license notices. Identify the source and
license of new dependencies, copied code, and assets for reviewer assessment.

[NOTICE]({{< repo "NOTICE" >}}) distinguishes adapted source from software
installed by image recipes and records unresolved provenance and distribution
work. CetiRun source headers or image labels do not relicense bundled programs,
dependencies, artwork, or external services. The notice is not a complete
per-image inventory or confirmation that redistribution obligations are met.
`make license-check` covers supported source files and Makefiles across the
repository, including site HTML templates and Python helpers. Markdown, YAML,
lockfiles, artwork, and documented upstream/generated exceptions remain
outside that mechanical check, not outside their license obligations.
See the [header procedure](../reference/scripts/#license-headers).

The Portal frontend build generates `THIRD_PARTY_LICENSES.txt` from its
production dependencies and embeds it with the UI. Missing required notices
fail the build unless an exact, verified supplement is recorded. Follow
[Portal notice generation](../reference/scripts/#portal-third-party-notices)
when updating frontend dependencies. That artifact covers neither the
documentation theme nor every runtime-image package.

The documentation site has its own third-party assets, distinct from runtime
images: Docsy, Bootstrap, jQuery, Lunr, and Font Awesome have their own notices
and license terms. See [the site source guide]({{< repo "site/README.md" >}})
for their license locations. Retain applicable notices with distributed
bundles. The original project logo is mounted into the site; do not assume
that a source header establishes artwork provenance or trademark permission.
