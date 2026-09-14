# CetiRun governance

CetiRun is an open-source managed runtime for hosting AI agents and AI applications.

This document describes how contributors and maintainers make project
decisions. It works alongside the [contribution guide](CONTRIBUTING.md),
[Code of Conduct](CODE_OF_CONDUCT.md), and [security policy](SECURITY.md).

## Roles and ownership

Anyone may report problems, suggest improvements, contribute changes, or help
review them under the contribution requirements and Code of Conduct.
Review feedback is welcome from contributors, but it does not by itself grant
merge, release, or repository administration permissions.

Maintainers are responsible for:

- project direction and compatibility of the runtime, operator, and portal;
- issue triage, technical review, and merge decisions;
- security response, dependency health, and release decisions;
- documentation, contributor experience, and conduct concerns; and
- keeping ownership and this governance document current.

Maintenance remains with the repository's existing maintainers in the Azure
organization. Repository administrators manage access and repository settings.
This document does not appoint new owners or change anyone's permissions.
Automatic code-owner review routing is not established by this baseline;
maintainers arrange review for the affected area.

## Decisions and review

Discuss ordinary changes in issues and pull requests. Record the problem,
alternatives considered, and reasons for decisions that affect contributors or
users. Keep those discussions public when they are safe to publish.
Confidential information and vulnerability details follow private reporting
channels instead.

Discuss changes with broad or difficult-to-reverse effects before implementation.
These include CRDs and API contracts, authentication and authorization,
instance isolation, persistent storage, new runtime dependencies, and support
or governance policy. An existing agreed proposal is sufficient; contributors
do not need to repeat the same approval process.

Maintainers seek agreement with affected contributors and reviewers. When
significant concerns remain unresolved, defer the change and record the open
questions. A maintainer should explain a decision to decline or narrow work.

Before merging:

- obtain approval from a maintainer other than the author;
- satisfy applicable CLA requirements and required checks;
- resolve review concerns and explain compatibility and operational effects; and
- confirm that the contribution and its evidence are safe to publish.

Request additional expertise for security-sensitive and cross-component changes.
Authors must not self-approve. If an independent reviewer is unavailable,
administrators should arrange one rather than waive review.

These are project review expectations, not a claim that every expectation is
already enforced by GitHub settings.

## Maintenance and releases

Maintainers decide what to accept, when to release, and which fixes can be
backported. [SUPPORT.md](SUPPORT.md) describes the limits of community support.
Only authorized maintainers and approved automation publish releases.
Contributing a change does not authorize publishing artifacts or modifying
live deployments.

Report vulnerabilities through [SECURITY.md](SECURITY.md). Maintainers
coordinate fixes and disclosure through the appropriate security process.
Passing a test or an automated scan alone is not evidence that the project
has no security issues.

## Ownership and governance changes

Contributors interested in maintaining an area can discuss it with the
maintainers. Candidates should show sustained useful contributions, careful
review, sound technical judgment, and respect for security and conduct.
Existing maintainers review the proposed responsibility, and repository
administrators handle any access changes.

When an ownership change is approved, update this document and the applicable
review-routing configuration together. Maintainers may step down;
administrators may also adjust access for inactivity, role changes, conduct
concerns, or security needs. Keep private personnel and security details out
of public discussions.

Changes to governance use the pull request process and require independent
maintainer review. Explain what changes and how it affects contributors.
