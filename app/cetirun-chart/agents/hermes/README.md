# Maintaining the Hermes runtime

CetiRun builds Hermes from an immutable upstream source release. The Dockerfile
downloads the selected GitHub tag and verifies the archive checksum. It makes
sure that the package version in `pyproject.toml` is correct. It installs the
repository's checked-in `uv.lock`. It then builds the upstream Dashboard and TUI
assets.

The reference for this release is the
[upstream v2026.9.24 Dockerfile](https://github.com/NousResearch/hermes-agent/blob/v2026.9.24/Dockerfile)
and [installer](https://github.com/NousResearch/hermes-agent/blob/v2026.9.24/scripts/install.sh).
CetiRun follows the container's `uv sync --frozen` path with the `all`,
`anthropic`, and `azure-identity` extras. It uses the upstream npm workspace
installation and Dashboard/TUI build commands. It does not run the interactive
host installer or replace CetiRun's HOME and process supervision.

CetiRun does not download the upstream Playwright browser, because the chart
supplies a separate Chrome/CDP desktop sidecar. UI assets build on the native
architecture of the builder with the upstream-pinned Node 26 image. Only their
architecture-independent output enters the target image. Python, SQLite, and
the final image build for `linux/amd64`. The runtime keeps Node 24.21.0 from
the shared base. This version is in the Node engine range of the upstream
package.

## Compatibility cleanup in 0.21.5

| Previous integration | Replacement |
| --- | --- |
| Checksum-verified PyPI wheel, `--no-deps` install, then a second `[all]` install | One source release and its checked-in `uv.lock` |
| Separate Azure Identity and PyJWT version overrides | Dependencies from the upstream lock and the `azure-identity` extra |
| Copilot helper that imports private Python functions | Public `hermes model` and `hermes auth status copilot` commands |
| Gateway shell pipeline and Dashboard `--insecure` | Supervisor-owned gateway process and the upstream Dashboard password provider |

The unrelated dependency fixes in the shared base stay unchanged. The pinned
SQLite build is kept because the SQLite in the base does not pass the WAL safety
check of this release. Its compiler, linker, C headers, Linux kernel headers,
and Make stay in a separate build stage. The runtime gets only the library.

## CetiRun-owned integration

CetiRun keeps these layers on top of the upstream source:

- The persistent user and HOME contract at UID/GID `1024` and
  `/home/vibe/.hermes`.
- Mandatory AI Gateway routing. Owners connect GitHub Copilot through the
  Portal, and the image has no platform direct-provider login service.
- Supervisor configuration for the Hermes gateway, Dashboard, terminal, and
  Tailscale.
- The external shared-skills directory and CetiRun-owned persona/context files.
- The SQLite version and compile flags that the upstream container uses to
  prevent the WAL-reset corruption bug on older distribution packages.
- Dashboard password authentication. The username is `cetirun`. The password is
  the `API_SERVER_KEY` that already exists. `CETIRUN_AUTH_KEY` signs Dashboard
  sessions.

The Chart supplies the Dashboard settings explicitly. Keys that are not set do
not disable upstream authentication.

Charts that disable the AI Gateway are not supported. The runtime rejects them
before initialization. It does not give an in-Agent model sign-in path instead.

Do not replace these contracts with the upstream container layout unless you
also qualify these items:

- persistent HOME ownership
- chart probes and ports
- Istio routes
- Portal authentication flows
- upgrade behavior

## Bounded long-running tools

The pinned Hermes release has an outer tool-execution deadline of 420 seconds.
If you increase only the requested `timeout` of a terminal tool, that outer
deadline does not increase. As a result, an eight-minute foreground command can
fail at 420 seconds, even when it requested 520 seconds. If the child process
writes a file later, this does not show that the foreground tool completed
successfully.

For a longer task that is explicitly approved, use a finite outer deadline that
is longer than the requested timeout of the tool. Also set an overall run
budget that is larger, but still finite. For example, use this command in the
normal owner terminal:

```bash
HERMES_CONCURRENT_TOOL_TIMEOUT_S=600 hermes chat \
  --oneshot -Q --max-turns 8 --run-budget 660 --query-file -
```

This override affects only that CLI process. The supported persistent
configuration is `timeouts.tools.concurrent_batch` in the Hermes configuration
that already exists. By default, `timeouts.tools.sequential_call` uses the
concurrent deadline. But you can set it independently. Explicit configuration
has priority over the environment bridge. If an override has no effect,
examine both.

Merge only the intended keys. Do not replace a retained configuration. Do not
show authentication files to diagnose timeouts.

Do not change the approval controls. Do not set these deadlines to zero or to a
negative value, because such a value disables them. Do not detach a task and
then report it as a foreground pass. Examine the native tool result and its
duration. Also examine an independent output.

Long tasks and lifecycle operations must have separate limits. A change to an
application deadline does not extend startup, restart, or migration.

## Instance URL prefixes

The Operator's Dashboard route owns `/ui/<instance>/`. Before it forwards a
request, it removes that prefix and supplies it in `X-Forwarded-Prefix`. The
Dashboard must use that value for browser requests, resources, and redirects.
After a browser sends an unprefixed `/auth/...` request, ingress cannot recover
the intended instance.

The Operator also supplies `HERMES_DASHBOARD_PUBLIC_URL` with the same HTTPS
origin and `/ui/<instance>` prefix. When managed ingress mTLS binds the backend
to loopback, this value gives the public hostname to the Dashboard's Host and
WebSocket Origin checks. It does not disable those checks or password
authentication. Under mTLS, `.env` assignments that stay in HOME cannot override
this Pod-owned value. This also applies after a supervised restart.

In 0.21.5, two upstream paths must be changed:

- The standalone login page submits to `/auth/password-login`.
- The default Vite build loads Chat's lazy JavaScript and CSS from `/assets/`.

Both paths omit the instance prefix. A rewrite of `fetch` alone cannot fix
browser-managed module preloads.

The image keeps the prefix at the points where URLs are generated:

- The [shared HTTP response adapter](compat/dashboard_prefix.py) uses upstream
  prefix validation. It inserts one `<base href="/ui/<instance>/">` in every
  HTML document. Root deployments get `<base href="/">`. The adapter also adds
  the prefix one time to origin-relative document assets, CSS references, and
  HTTP redirects.
- The Dashboard builds with Vite's [`--base=./` option](https://vite.dev/guide/build#relative-base).
  Initial assets resolve against the document base. Lazy imports and their CSS
  resolve relative to the module that imports them. The same build works at
  root, under an instance prefix, and on a nested route that is opened directly.
- The login template resolves its submission URL against `document.baseURI`.
  The shared auth completion path adds the validated prefix to local return
  paths. For API and WebSocket URLs, the SPA keeps the upstream
  `__HERMES_BASE_PATH__` logic.

There are no global fetch, XHR, WebSocket, or EventSource overrides. Hermes
continues to own cookies and authorization. Ingress ownership checks stay
unchanged. An origin-root request no longer has its instance identity. As a
result, ingress cannot repair it unless ingress guesses or adds shared routes.

The adapter processes UTF-8 HTML/CSS documents up to 8 MiB. Attachments, already
encoded bodies, non-UTF-8 content, JSON, SSE, and WebSocket messages pass
through, and the adapter does not rewrite their content. It does not interpret
arbitrary application JSON strings as URLs or rewrite JavaScript source text.

This pinned release has no HTTP middleware registration API. As a result,
[the build-time installer](compat/apply.py) changes three integration points:
the server wrapper, login submission, and the shared auth return path. Before
it changes any file, it verifies the original hashes of all three files. If the
upstream source changes, the image build fails and a review is necessary.
When an upstream release passes the root and prefixed browser regressions
without this adapter, remove the adapter. Do not replace the adapter with
per-endpoint ingress exceptions or routing guesses based on Referer or cookies.

## Upgrade procedure

1. Identify the current stable release on the
   [Hermes releases page](https://github.com/NousResearch/hermes-agent/releases).
2. Resolve the tag to its commit. Download the source archive that matches it.
3. Update `HERMES_VERSION`, `HERMES_RELEASE`, `HERMES_GIT_SHA`, and
   `HERMES_SOURCE_SHA256` together in `Dockerfile`.
4. Update `profile.yaml` to the same package version.
5. Compare the new upstream `pyproject.toml`, `uv.lock`, `package.json`, and
   `scripts/install.sh` with the current CetiRun integration. Do the same
   comparison for the new upstream Dockerfile, gateway/dashboard commands,
   plugin API, and configuration keys. Examine the pinned uv, Node, and SQLite
   images/sources and their checksums again.
6. Remove obsolete compatibility code only when the new public interface
   replaces it. Do not keep imports from private `hermes_cli` functions.
7. Regenerate the Hermes native render fixtures. Then run the checks that
   follow.

## Required checks

```bash
python3 -m unittest \
  scripts.tests.test_cetirun_image_dependencies \
  scripts.tests.test_harness_assets -v
make python-test
bash scripts/cetirun-helm-lint.sh
bash scripts/cetirun-helm-test.sh
go -C app/cetirun-operator test ./internal/agents/... ./internal/helm/... ./internal/controller/... -count=1
npm --prefix app/cetirun-portal/web test
npm --prefix app/cetirun-portal/web run build
```

Build the shared base and the Hermes image for `linux/amd64`. Qualify the
runtime in an isolated container with synthetic configuration and no external
network. We recommend that the runtime checks cover:

- the installed package version and the prebuilt Dashboard/TUI assets
- SQLite WAL persistence and FTS5 trigram search
- Gateway health and the web-terminal endpoint
- Dashboard rejection of unauthenticated requests and incorrect passwords,
  followed by password login and a session-authenticated request
- the CetiRun auth helper with and without its bearer key

These checks use synthetic files and credentials. For model responses, real
Copilot login, Chrome/CDP, Tailscale, and Kubernetes ingress, separately
authorized integrations are still necessary. These checks do not claim to
validate them.

```bash
docker buildx build --platform linux/amd64 \
  -f app/cetirun-chart/Dockerfile.base \
  -t cetirun-base:validation --load app/cetirun-chart
docker buildx build --platform linux/amd64 \
  --build-arg BASE_IMAGE=cetirun-base:validation \
  -f app/cetirun-chart/agents/hermes/Dockerfile \
  -t hermes-gateway:validation --load app/cetirun-chart
```

For rendered checks, use a synthetic local Dashboard behind an ingress-like
loopback proxy. Then examine these items:

- desktop and mobile login/logout, redirects, asset paths, and cookie isolation
  between two prefixes
- Chat opened from navigation, by direct URL, and after a full-page refresh
- terminal output and draft input echoed through the authenticated PTY
  WebSocket, without a prompt submitted to a model
- a nested profile route, and the same flows at root

When you reproduce the login or lazy CSS preload regression, include a control
with an image that fails. Keep screenshots and results outside the repository.
Use a new browser profile without real credentials.

The synthetic fixture has no Copilot credentials. Its side panel can show that
the model is not configured. Record that expected warning separately from
unexpected console warnings or errors. A terminal that renders and echoes draft
input does not prove that a model can respond.
