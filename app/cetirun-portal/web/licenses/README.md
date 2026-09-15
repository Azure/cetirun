# Version-pinned license supplements

The npm tarballs listed in `supplements.json` omit their upstream license file.
The generator uses these upstream copies only for the listed package versions,
and checks their SHA-256 before including them. The Fluent UI and Embla LICENSE
files are byte-for-byte copies. The Fluent UI NOTICE has a final newline added;
the Rollup license has trailing whitespace and its final newline normalized.

The source revisions are the `gitHead` values returned by the public npm
registry for `@fluentui/react-icons@2.0.340`, `embla-carousel@8.6.0`,
`embla-carousel-autoplay@8.6.0`, `embla-carousel-fade@8.6.0`, and
`@rollup/rollup-linux-x64-gnu@4.53.3`, verified on
2026-09-15. Fluent UI includes both LICENSE and NOTICE, preserved here. The Embla
root attribution-file inventory contains LICENSE; no additional root NOTICE,
COPYING, AUTHORS, or CONTRIBUTORS file was listed.

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

These copies preserve upstream copyright and terms. They do not establish
trademark permission for project artwork or cover other software in the images.
