// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServiceAccountTokenIsReloaded(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &kubeClient{tokenFile: tokenFile}
	first, err := client.serviceAccountToken()
	if err != nil || first != "first-token" {
		t.Fatalf("first token = %q, %v", first, err)
	}
	if err := os.WriteFile(tokenFile, []byte("second-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := client.serviceAccountToken()
	if err != nil || second != "second-token" {
		t.Fatalf("rotated token = %q, %v", second, err)
	}
	if err := os.WriteFile(tokenFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.serviceAccountToken(); err == nil {
		t.Fatal("empty projected token must be rejected")
	}
}

func TestWorkloadServiceAccountUsesInstanceName(t *testing.T) {
	if got := workloadServiceAccount("agentid-hermes-v1"); got != "openclaw-agentid-hermes-v1-cetirun" {
		t.Fatalf("workload ServiceAccount = %q", got)
	}
	if got := workloadServiceAccount("a-very-long-instance-name-that-needs-to-be-truncated-at-the-dns-limit"); len(got) > 63 || got[len(got)-1] == '-' {
		t.Fatalf("workload ServiceAccount is not a valid bounded name: %q", got)
	}
}
