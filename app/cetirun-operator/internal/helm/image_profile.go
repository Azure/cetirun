// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents/builtin"
	"github.com/Masterminds/semver/v3"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/registry"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

const maxProfileManifest = 64 << 10

type profileTransport struct{}

func (profileTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Check all registry, token service and redirect requests, not only the
	// initial manifest URL. Admission never falls back to cleartext.
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("native image profile lookup requires HTTPS")
	}
	return http.DefaultTransport.RoundTrip(req)
}

// ReadImageProfile inspects only the operator-selected OCI repository. The
// admission process has a read-only filesystem and must not read Helm release
// Secrets, render a chart, or contact the image registry supplied by a caller.
func (c *Client) ReadImageProfile(ctx context.Context, version, kind, imageRegistry, credentialsFile string) (agents.Profile, error) {
	var empty agents.Profile
	if version == "" {
		version = c.chartRef.Version
	}
	if _, err := semver.StrictNewVersion(version); err != nil || len(version) > 128 {
		return empty, agents.ErrImageProfileVersion
	}
	harness, err := builtin.NewRegistry().Lookup(kind)
	if err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(ctx, chartInspectionTimeout)
	defer cancel()
	// Helm encodes SemVer build metadata with underscores in OCI tags.
	ref := strings.ReplaceAll(strings.TrimPrefix(c.ChartIdentity(version), "oci://"), "+", "_")
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return empty, fmt.Errorf("invalid configured chart repository")
	}
	client := &auth.Client{Client: &http.Client{Transport: profileTransport{}}}
	if credentialsFile != "" {
		store, err := credentials.NewStore(credentialsFile, credentials.StoreOptions{})
		if err != nil {
			return empty, fmt.Errorf("cannot initialize the configured chart-read credential")
		}
		client.Credential = credentials.Credential(store)
	}
	repo.Client = client
	repo.MaxMetadataBytes = maxProfileManifest
	archive, err := profileArchive(ctx, repo, repo.Reference.Reference)
	if err != nil {
		return empty, err
	}
	chrt, err := loader.LoadArchive(bytes.NewReader(archive))
	if err != nil || chrt.Metadata == nil || chrt.Name() != "cetirun" || chrt.Metadata.Version != version {
		return empty, fmt.Errorf("native image profile chart metadata does not match the selected version")
	}
	return loadRuntimeProfile(chrt, harness.Describe(), imageRegistry)
}

// Bound the bytes verified and expanded on an admission request. Only the
// Helm layer is needed; never follow an unrelated artifact or image layer.
func profileArchive(ctx context.Context, source oras.ReadOnlyTarget, ref string) ([]byte, error) {
	const maxArchive, maxExpanded = 4 << 20, 8 << 20
	desc, raw, err := oras.FetchBytes(ctx, source, ref, oras.FetchBytesOptions{MaxBytes: maxProfileManifest})
	if err != nil {
		return nil, profileFetchError(ctx, err)
	}
	var manifest ocispec.Manifest
	if desc.MediaType != ocispec.MediaTypeImageManifest || json.Unmarshal(raw, &manifest) != nil ||
		manifest.SchemaVersion != 2 || manifest.Config.MediaType != registry.ConfigMediaType {
		return nil, fmt.Errorf("expected a Helm OCI chart manifest")
	}
	var chartLayer *ocispec.Descriptor
	for i, layer := range manifest.Layers {
		if layer.MediaType == registry.ChartLayerMediaType || layer.MediaType == registry.LegacyChartLayerMediaType {
			if chartLayer != nil || layer.Size <= 0 || layer.Size > maxArchive {
				return nil, fmt.Errorf("ambiguous or oversized Helm chart layer")
			}
			chartLayer = &manifest.Layers[i]
		}
	}
	if chartLayer == nil {
		return nil, fmt.Errorf("missing Helm chart layer")
	}
	archive, err := content.FetchAll(ctx, source, *chartLayer)
	if err != nil {
		return nil, profileFetchError(ctx, err)
	}
	unpacked, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("invalid Helm chart compression")
	}
	defer unpacked.Close()
	if count, err := io.Copy(io.Discard, io.LimitReader(unpacked, maxExpanded+1)); err != nil || count > maxExpanded {
		return nil, fmt.Errorf("invalid or oversized Helm chart archive")
	}
	return archive, nil
}

func profileFetchError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Registry errors can quote signed URLs. Log only the classification.
	return fmt.Errorf("cannot fetch native image profile (%s)", (&ChartDownloadError{Err: err}).Class())
}
