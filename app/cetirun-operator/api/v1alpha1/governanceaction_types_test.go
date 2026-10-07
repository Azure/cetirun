// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestGovernanceActionDigestCanonicalizesApprovalPayload(t *testing.T) {
	spec := GovernanceActionSpec{
		Requester:           GovernanceActionRequester{Kind: "CetiAdmin", Subject: "requester"},
		TargetRef:           GovernanceActionTargetRef{APIVersion: GroupVersion.String(), Kind: "Agent", Name: "agent", UID: types.UID("uid"), Generation: 3},
		Operation:           GovernanceActionRestart,
		ExpectedProjections: GovernanceActionProjections{AuthorizationRevision: "authorization"},
		Reason:              "roll out a revised policy",
		RequestedAt:         metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("UTC+8", 8*60*60))),
		ExpiresAt:           metav1.NewTime(time.Date(2026, 1, 2, 4, 4, 5, 6, time.FixedZone("UTC+8", 8*60*60))),
	}
	first, err := GovernanceActionDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Approval = &GovernanceActionApproval{Decision: "Approved", ActionDigest: "untrusted"}
	second, err := GovernanceActionDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("approval must not alter the proposal digest")
	}
	spec.TargetRef.Generation++
	third, err := GovernanceActionDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatal("target generation must be bound by the proposal digest")
	}
}
