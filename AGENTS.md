# Working on CetiRun

CetiRun is an open-source managed runtime for hosting AI agents and AI applications.

Read [CONTRIBUTING.md](CONTRIBUTING.md), [DEVELOPMENT.md](DEVELOPMENT.md), and
[SECURITY.md](SECURITY.md) before changing the repository.

## Scope and collaboration

- Verify the requested checkout, branch, and working-tree state before editing.
  Use the task's explicit worktree when one is supplied.
- Preserve other contributors' changes. Edit only the files assigned to the
  task and coordinate overlapping work instead of reverting it.
- Follow an already-approved scope without reopening the same design decision.
  Ask before making changes that expand that scope.
- Use meaningful branch names such as `fix/portal-session-list`,
  `feat/instance-status`, and `docs/local-development`.
- Do not commit, push, open or merge pull requests, publish releases, change
  repository settings, or modify live resources unless explicitly requested.

## Implementation and checks

- Follow existing component conventions and keep changes focused.
- Use the validation entry points in [DEVELOPMENT.md](DEVELOPMENT.md).
  Where the root Makefile is available, use `make check`, `make test`,
  `make lint`, and `make docs-check`. For an earlier checkout, use the
  documented component commands and report the checks actually available.
- Start with offline tests and local chart rendering. Do not run deployment
  scripts, release commands, real-cluster tests, or live service integrations
  without explicit authorization for the target environment.
- Inspect commands before running them. A build or test task does not authorize
  a cloud login, dependency upgrade, unpinned generator, or cluster mutation.
- Treat missing dependencies as a reported limitation. Do not replace a failing
  check with a weaker one without explaining the difference.
- Regenerate checked-in artifacts from their source using an agreed tool
  version; do not hand-edit generated CRDs or deepcopy code.
- Keep build output, caches, local configuration, and unrelated formatting out
  of the diff.

## Versioning and publishing

- `app/cetirun-chart/Chart.yaml` is the default release-version source. For an
  official version change, run `make set-version VERSION=X.Y.Z`; do not update
  individual Chart, Go, Portal, or Teams version fields by hand.
- Validate the synchronized source with `make check`. Operator and Portal Go
  binaries receive the release version and Git commit during Makefile, Docker,
  and release-workflow builds.
- After explicit authorization and registry authentication, publish everything
  with `make publish REGISTRY=registry.example.com`. Use `make publish-images`
  or `make publish-charts` when only one artifact class is requested.
- Publishing includes the `cetirun-pi-agent` demo and verifies Chart pulls.
  It does not log in to the registry, deploy to Kubernetes, or authorize
  changing another registry or environment.

## Public-data and review boundary

- Use synthetic fixtures and public example domains. Do not add credentials,
  tokens, cookies, authorization headers, private endpoints, customer data,
  agent conversations, or internal identifiers and documents.
- Do not read credential stores, token files, browser cookies, or live customer
  data during routine development. Any sensitive diagnostic work needs a
  separately authorized scope.
- Route suspected vulnerabilities through [SECURITY.md](SECURITY.md), not
  public issues or pull requests.
- Do not invent owner accounts or teams. Changes to review ownership require
  maintainer confirmation; see [GOVERNANCE.md](GOVERNANCE.md).
- Maintainers decide whether to accept a contribution. Agent output does not
  substitute for independent maintainer review.
- At handoff, list changed paths, commands and results, remaining limitations,
  and checks not run. Distinguish source inspection, offline validation, and
  live verification. Do not claim that limited tests establish security.
