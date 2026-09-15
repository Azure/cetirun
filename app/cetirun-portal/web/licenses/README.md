# Version-pinned license supplements

The npm tarballs listed in `supplements.json` omit their upstream license file.
The generator uses these upstream copies only for the listed package versions,
and checks their SHA-256 before including them. LICENSE files are byte-for-byte
copies; the Fluent UI NOTICE has a final newline added.

The source revisions are the `gitHead` values returned by the public npm
registry for `@fluentui/react-icons@2.0.340`, `embla-carousel@8.6.0`,
`embla-carousel-autoplay@8.6.0`, and `embla-carousel-fade@8.6.0`, verified on
2026-09-15. Fluent UI includes both LICENSE and NOTICE, preserved here. The Embla
root attribution-file inventory contains LICENSE; no additional root NOTICE,
COPYING, AUTHORS, or CONTRIBUTORS file was listed.

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
