# Pre-proxy native upgrade fixture

[pre-proxy.json](pre-proxy.json) was produced by the controller and runtime
renderer at commit `6414494154151b2daf74d39d1e989ebeb7ea5020`, before native
ingress proxy projection was added. It is not a fixture authored by the new
compiler with its revision rewritten to look old.

This baseline uses the current single-policy API and `v1` authorization,
expectation and revision formats. It does not assert a rolling migration from
the removed dual-policy prototype. The chart compatibility tests verify the
pre-proxy assets from this same source revision.

The exporter runs that revision's existing `nativeSandboxFixture` to Ready,
then captures its API objects, rendered OpenClaw configuration, workload,
source digests, preparation receipt, consumed single-start marker and Gateway
test-double state. Kubernetes controller observations and Gateway responses are
synthetic; this fixture does not claim an actual application or cluster run.

Random bytes are fixed to `0x42`. The file replaces only the resulting dummy
Secret data and the checksum of an empty template render with named markers.
The test reconstructs those values and verifies the original object-set SHA-256
before loading it. No live credentials or deployment data are used.

## Regeneration

Use Go 1.27.3 and the separately prepared dependencies described in
[DEVELOPMENT.md](../../../../../../DEVELOPMENT.md). From the current checkout,
choose a new temporary directory:

```bash
source_root="$(git rev-parse --show-toplevel)"
fixture_dir="$source_root/app/cetirun-operator/internal/controller/testdata/native-upgrade"
old_root="$(mktemp -d)"
git clone --no-hardlinks --no-checkout "$source_root" "$old_root/repo"
git -C "$old_root/repo" checkout --detach 6414494154151b2daf74d39d1e989ebeb7ea5020
cp "$fixture_dir/export_test.go" \
  "$old_root/repo/app/cetirun-operator/internal/controller/export_native_upgrade_test.go"
CETIRUN_UPGRADE_FIXTURE_OUT="$fixture_dir/pre-proxy.json" \
  GOTOOLCHAIN=go1.27.3 GOPROXY=off GOFLAGS=-mod=readonly \
  go -C "$old_root/repo/app/cetirun-operator" test ./internal/controller \
    -run '^TestExportNativeUpgradeFixture$' -count=1
```

The exporter verifies the checkout revision before collecting any fixture data.
Review the regenerated file before committing it. Observation timestamps can
differ between runs; the controller/compiler provenance and complete captured
state must remain verifiable. Remove only the temporary checkout you created
afterward.
