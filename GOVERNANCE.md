# CetiRun governance

CetiRun is an open-source managed runtime for hosting AI agents and AI applications.

This document describes how contributors and maintainers make project
decisions. It applies together with the [contribution guide](CONTRIBUTING.md),
the [Code of Conduct](CODE_OF_CONDUCT.md), and the
[security policy](SECURITY.md).

## Roles and ownership

Anyone can report problems, suggest improvements, contribute changes, or help
review them. The contribution requirements and the Code of Conduct apply to
this work. Review feedback from contributors is welcome, but the feedback alone
does not grant merge, release, or repository administration permissions.

Maintainers are responsible for these items:

- Project direction and the compatibility of the runtime, operator, and portal
- Issue triage, technical review, and merge decisions
- Security response, dependency health, and release decisions
- Documentation, contributor experience, and conduct concerns
- Updates that keep ownership and this governance document current

Maintenance stays with the current maintainers of the repository in the Azure
organization. Repository administrators manage access and repository settings.
This document does not appoint new owners or change anyone's permissions. This
baseline does not include automatic code-owner review routing. Maintainers
arrange the review for the affected area.

## Decisions and review

Discuss normal changes in issues and pull requests. Record the problem, the
alternatives that were considered, and the reasons for decisions that affect
contributors or users. When those discussions are safe to publish, keep them
public. For confidential information and vulnerability details, use private
channels to report them instead.

Before implementation, discuss changes that have broad effects or effects that
are difficult to reverse. These include CRDs and API contracts, authentication
and authorization, instance isolation, persistent storage, new runtime
dependencies, and support or governance policy. If an agreed proposal already
exists, that proposal is enough. It is not necessary for contributors to repeat
the same approval process.

Maintainers try to get agreement with the affected contributors and reviewers.
If important concerns are still not resolved, defer the change and record the
open questions. We recommend that a maintainer explains a decision to decline
work or to decrease its scope.

Before a merge, do these tasks:

- Get approval from a maintainer other than the author.
- Satisfy the applicable CLA requirements and required checks.
- Resolve the review concerns. Explain the compatibility and operational
  effects.
- Make sure that the contribution and its evidence are safe to publish.

For security-sensitive and cross-component changes, request more expertise.
Authors must not approve their own changes. If no independent reviewer is
available, we recommend that administrators arrange one and do not waive the
review.

These items are project review expectations. They are not a claim that GitHub
settings already enforce every expectation.

## Maintenance and releases

Maintainers decide what to accept, when to release, and which fixes can be
backported. [SUPPORT.md](SUPPORT.md) describes the limits of community support.
Only authorized maintainers and approved automation publish releases.
Contributing a change does not authorize you to publish artifacts or change
live deployments.

Report vulnerabilities through [SECURITY.md](SECURITY.md). Maintainers
coordinate fixes and disclosure through the applicable security process. If a
test or an automated scan passes, this result alone is not evidence that the
project has no security issues.

## Ownership and governance changes

Contributors who want to become maintainers of an area can discuss this with
the maintainers. We recommend that candidates show sustained useful
contributions, careful review, good technical judgment, and respect for
security and conduct. The current maintainers review the proposed
responsibility. Repository administrators handle any access changes.

When an ownership change is approved, update this document and the applicable
review-routing configuration together. Maintainers can resign. Administrators
can also change access because of inactivity, role changes, conduct concerns,
or security reasons. Keep private personnel and security details out of public
discussions.

Changes to governance use the pull request process. They must have independent
maintainer review. Explain what changes and how the change affects
contributors.
