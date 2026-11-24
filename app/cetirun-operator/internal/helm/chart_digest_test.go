// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/registry"
)

func TestRuntimeChartDigestSurvivesTagReplacement(t *testing.T) {
	// This real loopback OCI registry replaces the current tag, but keeps the
	// originally verified manifest available by digest. Older Agent versions
	// must continue to resolve their own tag.
	digest := func(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }
	blobs, manifests := map[string][]byte{}, map[string][]byte{}
	makeChart := func(version, marker string) string {
		t.Helper()
		ch := &chart.Chart{Metadata: &chart.Metadata{Name: "cetirun", Version: version, APIVersion: "v2"},
			Values: map[string]interface{}{"marker": marker},
			Raw:    []*chart.File{{Name: "values.yaml", Data: []byte("marker: " + marker + "\n")}}}
		filename, err := chartutil.Save(ch, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		layer, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		config, _ := json.Marshal(ch.Metadata)
		blobs[digest(layer)], blobs[digest(config)] = layer, config
		manifest, _ := json.Marshal(map[string]interface{}{
			"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
			"config": map[string]interface{}{"mediaType": registry.ConfigMediaType, "digest": digest(config), "size": len(config)},
			"layers": []interface{}{map[string]interface{}{"mediaType": registry.ChartLayerMediaType, "digest": digest(layer), "size": len(layer)}},
		})
		manifests[digest(manifest)] = manifest
		return digest(manifest)
	}
	pin := makeChart("1.0.0", "verified")
	replacement := makeChart("1.0.0", "replacement")
	older := makeChart("0.9.0", "older")
	manifests["1.0.0"], manifests["0.9.0"] = manifests[replacement], manifests[older]
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		var body []byte
		if ref, ok := strings.CutPrefix(r.URL.Path, "/v2/helm/cetirun/manifests/"); ok {
			body = manifests[ref]
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		} else if ref, ok := strings.CutPrefix(r.URL.Path, "/v2/helm/cetirun/blobs/"); ok {
			body = blobs[ref]
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		if body == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Docker-Content-Digest", digest(body))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	}))
	defer server.Close()
	reg, err := registry.NewClient(registry.ClientOptPlainHTTP())
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(ChartReference{RepoURL: "oci://" + strings.TrimPrefix(server.URL, "http://") + "/helm/cetirun", Version: "1.0.0", Digest: pin})
	for _, version := range []string{"", "1.0.0"} {
		if got := client.ChartIdentity(version); got != client.chartRef.RepoURL+"@"+pin {
			t.Fatalf("pinned chart cache identity = %s", got)
		}
	}
	if got := client.ChartIdentity("0.9.0"); got != client.chartRef.RepoURL+":0.9.0" {
		t.Fatalf("older chart identity = %s", got)
	}
	for _, tc := range []struct{ version, marker string }{{"", "verified"}, {"1.0.0", "verified"}, {"0.9.0", "older"}} {
		got, err := client.loadChart(&action.Configuration{RegistryClient: reg}, "synthetic", tc.version)
		if err != nil {
			t.Fatal(err)
		}
		if got.Values["marker"] != tc.marker {
			t.Fatalf("version %q used %v, want %s", tc.version, got.Values["marker"], tc.marker)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, request := range requests {
		if strings.HasSuffix(request, "/manifests/1.0.0") {
			t.Fatalf("pinned runtime queried a mutable tag: %s", request)
		}
	}
}

func TestChartReferenceDigestValidation(t *testing.T) {
	for _, pin := range []string{"sha256:short", "sha256:" + strings.Repeat("A", 64), "other:" + strings.Repeat("a", 64)} {
		ref := ChartReference{RepoURL: "oci://registry.example.com/helm/cetirun", Version: "1.0.0", Digest: pin}
		if err := ref.Validate(); err == nil {
			t.Fatalf("accepted invalid digest %q", pin)
		}
	}
}
