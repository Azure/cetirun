# Version-pinned license supplements

The npm tarballs listed in `supplements.json` omit their upstream license file.
The generator uses these upstream copies only for the listed package versions,
and checks their SHA-256 before including them. The Fluent UI and Embla LICENSE
files are byte-for-byte copies. The Fluent UI NOTICE has a final newline added;
the Rollup license has trailing whitespace and its final newline normalized.

The Fluent UI source revision is the public npm `gitHead` for
`@fluentui/react-icons@2.0.343`, verified on 2026-10-06. The integrity-checked npm
tarball contains no license or notice files. Its upstream LICENSE is unchanged;
its NOTICE matches the stored copy after adding the final newline. Both source
URLs point to that version's commit.

The Embla and Rollup source revisions are the public npm `gitHead` values for
`embla-carousel@8.6.0`, `embla-carousel-autoplay@8.6.0`,
`embla-carousel-fade@8.6.0`, and `@rollup/rollup-linux-x64-gnu@4.53.3`, verified
on 2026-09-15. The Embla root attribution-file inventory contains LICENSE; no
additional root NOTICE, COPYING, AUTHORS, or CONTRIBUTORS file was listed.

The Rollup native package is an optional dependency of `tabster@8.7.0`, installed
on Linux x64 but absent on ARM64. Its supplement preserves the upstream
LICENSE.md, including the core license and bundled dependency notices.
The original download SHA-256 before whitespace normalization is
`fa1bd040c5bdeefe65b3821cebf474f2733ce65df13089bd151dda1778e62fe8`.

When upgrading one of these packages:

1. Check whether the new npm tarball includes its license and additional notices.
2. If it does not, verify its source repository and release revision, download
   the applicable upstream license/attribution files into a temporary directory,
   and review their applicability to that package.
3. Update the version-specific entry, source URL, license copy, and SHA-256
   together. Do not extend an old supplement to a new version without review.
4. Run `npm run test:licenses`, `npm run licenses`, and `npm run build`.

For an updated version of a package already listed here, the helper can prepare
the metadata part of step 3 without changing source files:

```bash
npm run licenses:check-updates -- --output /tmp/portal-license-update.patch
```

Use a new output path outside the web directory. On hosts that require an
HTTP(S) proxy, prefix this networked command with `NODE_USE_ENV_PROXY=1`.
The helper checks locked production versions, including platform-only optional
packages. It reads public npm metadata and commit-pinned GitHub files without
installing or executing package code. A proposal requires matching package
identity and integrity, the same repository and license declaration, an unchanged
attribution-file inventory, and unchanged text after CRLF and final-whitespace
normalization. Unknown packages, changed terms or notices, and unverified sources
need manual review.
The helper does not inspect tarball contents; review those before applying a
proposal. It does not approve a dependency.

Exit 0 means known supplements already cover the locked versions; this path
makes no network requests. Exit 1 means review is needed or a request failed.
Only a verified unchanged-text update produces a patch. Portal CI uploads that
patch as `portal-license-update` and remains failed until a reviewed update is
included in the PR. It never commits or pushes changes. The normal build and
notice generator remain offline and require exact package versions.

These copies preserve upstream copyright and terms. They do not establish
trademark permission for project artwork or cover other software in the images.
