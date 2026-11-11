// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxpolicy

import (
	"errors"
	"fmt"
	"testing"
)

func TestPublicDetailReturnsOnlyFixedExplanations(t *testing.T) {
	failure := ControlFailure(ErrUnavailable, "a fixed explanation")
	if !errors.Is(failure, ErrUnavailable) || failure.Error() != ErrUnavailable.Error()+": a fixed explanation" {
		t.Fatalf("a public failure changed its class or text: %v", failure)
	}
	quoted := fmt.Errorf("%w: value-canary-5d1f", ErrConflict)
	wrapped := fmt.Errorf("registration: %w", errors.Join(quoted, failure))
	for _, tc := range []struct {
		name     string
		sentinel error
		want     string
	}{
		{"same class", ErrUnavailable, "a fixed explanation"},
		{"every class", nil, "a fixed explanation"},
		{"other class", ErrConflict, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PublicDetail(wrapped, tc.sentinel); got != tc.want {
				t.Fatalf("PublicDetail = %q, want %q", got, tc.want)
			}
		})
	}
	if got := PublicDetail(quoted, nil); got != "" {
		t.Fatalf("text that is not a fixed explanation became public: %q", got)
	}
}

func TestRegistrationFailuresHaveFixedReasons(t *testing.T) {
	input := rolloutEgressFixture()
	input.Platform.Topology.PodCIDRs = nil
	if err := ValidatePlatform(input.Platform, input.Resolution.Capabilities); PublicDetail(err, ErrUnavailable) != "pod exclusion inventory is required" {
		t.Fatalf("a missing inventory has no fixed reason: %v", err)
	}
	for _, tc := range []struct {
		identity ApprovedIdentity
		want     string
	}{
		{ApprovedIdentity{}, "approvals require a complete Agent binding and an unshared dedicated ServiceAccount"},
		{ApprovedIdentity{AgentUID: "example-agent-uid", Namespace: "example", ServiceAccountName: "example-agent", Revision: "1",
			Entra: &EntraIdentity{}}, "Entra approval requires explicit client and tenant identifiers"},
	} {
		if err := ValidateIdentityApprovals([]ApprovedIdentity{tc.identity}); PublicDetail(err, ErrUnapprovedGrant) != tc.want {
			t.Fatalf("an identity approval failure has no fixed reason: %v", err)
		}
	}
}
