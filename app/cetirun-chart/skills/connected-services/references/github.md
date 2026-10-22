# GitHub REST API

Service: `github`. Origin: `https://api.github.com`. Base path: `/`.

- GET `/repos/{owner}/{repo}` for repository metadata.
- GET `/repos/{owner}/{repo}/issues`, query `{"state":"open","per_page":"20"}`.
  GitHub's issues endpoint also returns pull requests; entries with
  `pull_request` are PRs. Filter those when the user asks specifically for issues.
- GET `/repos/{owner}/{repo}/pulls`, query `{"state":"open","per_page":"20"}`.
- GET `/repos/{owner}/{repo}/contents/{path}` for JSON content metadata; file
  text may be base64 in `body.content`. Raw/binary media is unsupported.
- POST `/repos/{owner}/{repo}/issues`, body `{"title":"Example","body":"Details"}`.
- POST `/repos/{owner}/{repo}/pulls`, body includes `title`, `head` and `base`.

These routes use repository owner/name; a numeric repository ID is not required.
Use the returned `Link` header for pagination after validating and converting
its URL as described in the main skill. The optional `X-GitHub-Api-Version`
header selects a supported API version.

Current connections use a personal OAuth account. The OAuth App's `repo` scope
is broad; the user's repository rights and organization OAuth/SSO restrictions
still apply. CetiRun does not narrow access to selected repositories or tools.
GitHub records the connected account as the actor; CetiRun also records which
Agent used the connection. A failed response after creating an issue or PR can
leave the operation's outcome unknown; check for its result before resubmitting.
