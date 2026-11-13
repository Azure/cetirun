// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxpolicy

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/egresscapture"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
)

// valueCanary is a synthetic value that a fixed reason must never quote.
const valueCanary = "value-canary-5d1f"

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

// TestWithPublicDetailKeepsTheErrorForLogs checks that a fixed explanation
// can be added to an error, for example an API error, without a change to its
// text or its class.
func TestWithPublicDetailKeepsTheErrorForLogs(t *testing.T) {
	if WithPublicDetail(nil, "a fixed explanation") != nil {
		t.Fatal("no failure became a failure")
	}
	missing := apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, valueCanary)
	cause := fmt.Errorf("read: %w", missing)
	failure := WithPublicDetail(cause, "a fixed explanation")
	if failure.Error() != cause.Error() || !apierrors.IsNotFound(failure) || errors.Is(failure, ErrUnavailable) {
		t.Fatalf("the failure changed its text or class: %v", failure)
	}
	if PublicDetail(failure, nil) != "a fixed explanation" || PublicDetail(fmt.Errorf("outer: %w", failure), nil) != "a fixed explanation" {
		t.Fatalf("the fixed explanation is not public: %v", failure)
	}
	if PublicDetail(failure, ErrUnavailable) != "" {
		t.Fatal("the fixed explanation was returned for another class")
	}
	classified := WithPublicDetail(fmt.Errorf("%w: %s", ErrConflict, valueCanary), "a fixed conflict")
	if !errors.Is(classified, ErrConflict) || PublicDetail(classified, ErrConflict) != "a fixed conflict" {
		t.Fatalf("a classified failure lost its class or explanation: %v", classified)
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

// requireFixedReason checks that err has the class and the fixed reason, and
// that neither the reason nor the error text quotes the synthetic value.
func requireFixedReason(t *testing.T, err, class error, reason string) {
	t.Helper()
	if !errors.Is(err, class) || PublicDetail(err, class) != reason || PublicDetail(err, nil) != reason {
		t.Fatalf("the failure has no fixed reason %q: %v", reason, err)
	}
	if err.Error() != class.Error()+": "+reason || strings.Contains(err.Error(), valueCanary) {
		t.Fatalf("the failure text is not only its fixed reason: %v", err)
	}
}

// TestIngressMTLSRegistrationFailuresHaveFixedReasons checks every message of
// the ingress mTLS registration check. These messages are constant text, so
// the status shows them; a registered value must never be part of them.
func TestIngressMTLSRegistrationFailuresHaveFixedReasons(t *testing.T) {
	ref := func(namespace, name string) ingressmtls.Reference {
		return ingressmtls.Reference{Namespace: namespace, Name: name, UID: types.UID("synthetic-" + name)}
	}
	chat := func(binding *ingressmtls.Binding, labels map[string]string) {
		binding.Harness = "custom"
		binding.ChatClients = []ingressmtls.ChatClient{{Secret: ref(valueCanary, "portal-client"),
			URI: "spiffe://example.com/portal/" + string(binding.AgentUID), PodLabels: labels}}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ingressmtls.Registration)
		reason string
	}{
		{"tag-only image", func(r *ingressmtls.Registration) { r.Image = "registry.example.com/" + valueCanary + ":latest" },
			"mTLS registration requires a digest-pinned image and bounded bindings"},
		{"unsupported harness", func(r *ingressmtls.Registration) { r.Bindings[0].Harness = valueCanary },
			"mTLS requires a per-Agent supported harness and nonconflicting primary port"},
		{"Hermes Webapp port", func(r *ingressmtls.Registration) { r.Bindings[0].Harness, r.Bindings[0].Port = "hermes", 9120 },
			"Hermes API port conflicts with its Webapp port"},
		{"Chat client of a built-in harness", func(r *ingressmtls.Registration) {
			r.Bindings[0].ChatClients = []ingressmtls.ChatClient{{Secret: ref(valueCanary, "portal-client")}}
		}, "mTLS Custom supports declared HTTP and terminal endpoints, not desktop or unrelated Chat clients"},
		{"incomplete reference", func(r *ingressmtls.Registration) {
			r.Bindings[0].ServerSecret.Name, r.Bindings[0].ServerSecret.UID = valueCanary, ""
		}, "mTLS references require explicit namespace, name and UID, and an optional exact resourceVersion"},
		{"server credentials outside the Agent", func(r *ingressmtls.Registration) { r.Bindings[0].ServerSecret.Namespace = valueCanary },
			"mTLS server credentials belong to the Agent; client credentials belong only to its external Gateway"},
		{"client URI of another Agent", func(r *ingressmtls.Registration) {
			r.Bindings[0].ClientURI = "spiffe://" + valueCanary + ".example.com/gateway/other-agent"
		}, "mTLS requires an exact client SPIFFE URI SAN ending in the Agent UID"},
		{"Chat client without a selector", func(r *ingressmtls.Registration) { chat(&r.Bindings[0], nil) },
			"mTLS Chat requires distinct per-namespace client identities and explicit Pod selectors"},
		{"invalid Chat client selector", func(r *ingressmtls.Registration) {
			chat(&r.Bindings[0], map[string]string{valueCanary + "/not/a/label": "portal"})
		}, "mTLS Chat client selector is invalid"},
		{"duplicate Agent binding", func(r *ingressmtls.Registration) { r.Bindings = append(r.Bindings, r.Bindings[0]) },
			"mTLS Agent bindings must be unique"},
		{"shared Secret approval", func(r *ingressmtls.Registration) {
			other := r.Bindings[0]
			other.AgentUID = "example-other-agent"
			other.ClientURI = "spiffe://example.com/gateway/" + string(other.AgentUID)
			r.Bindings = append(r.Bindings, other)
		}, "mTLS Secret approvals must be distinct and per-Agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := ingressMTLSFixture(t, "openclaw")
			if err := ValidatePlatform(input.Platform, input.Resolution.Capabilities); err != nil {
				t.Fatal("the fixture registration is not valid:", err)
			}
			tc.mutate(input.Platform.IngressMTLS)
			requireFixedReason(t, ValidatePlatform(input.Platform, input.Resolution.Capabilities), ErrUnavailable, tc.reason)
		})
	}
}

func TestPlatformCapabilityFailuresHaveFixedReasons(t *testing.T) {
	sni := withEgressSNI(ingressMTLSFixture(t, "openclaw"))
	sni.Platform.IngressMTLS, sni.Resolution.Capabilities.MutualTLS = nil, false
	if err := ValidatePlatform(sni.Platform, sni.Resolution.Capabilities); err != nil {
		t.Fatal("the fixture registration is not valid:", err)
	}
	sni.Platform.EgressSNI = &egresscapture.Registration{EnvoyImage: "registry.example.com/" + valueCanary + ":latest", InitImage: valueCanary}
	requireFixedReason(t, ValidatePlatform(sni.Platform, sni.Resolution.Capabilities), ErrUnavailable,
		"observed-SNI registration requires digest-pinned Envoy and trusted capture-init images")

	for _, tc := range []struct {
		name   string
		mutate func(*sandbox.Capabilities)
		reason string
	}{
		{"unknown profile", func(c *sandbox.Capabilities) { c.Profile = valueCanary }, "unsupported Sandbox capability profile"},
		{"unknown HOME access mode", func(c *sandbox.Capabilities) {
			c.HomeAccessModes = append(c.HomeAccessModes, corev1.PersistentVolumeAccessMode(valueCanary))
		}, "unsupported Sandbox capability profile"},
		{"missing network control", func(c *sandbox.Capabilities) { c.DenyPrecedence = false },
			"the registration does not support every mandatory network control"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := nativeCompilerFixture()
			if err := ValidatePlatform(input.Platform, input.Resolution.Capabilities); err != nil {
				t.Fatal("the fixture registration is not valid:", err)
			}
			tc.mutate(&input.Resolution.Capabilities)
			requireFixedReason(t, ValidatePlatform(input.Platform, input.Resolution.Capabilities), ErrUnsupportedControl, tc.reason)
		})
	}
}

// TestPolicyBudgetFailuresHaveFixedReasons checks that a policy that exceeds a
// compiler budget names the budget and its constant limit, never an input.
func TestPolicyBudgetFailuresHaveFixedReasons(t *testing.T) {
	remaining := 1
	requireFixedReason(t, consumeBudget(&remaining, 2, "peer selector expansion"), ErrConflict, "peer selector expansion budget exceeded")

	input := compilerFixture()
	input.Resolution.AgentPolicy.Network.BlockedCIDRs = make([]string, MaxControlInputEntries+1)
	for i := range input.Resolution.AgentPolicy.Network.BlockedCIDRs {
		input.Resolution.AgentPolicy.Network.BlockedCIDRs[i] = valueCanary
	}
	policy := &api.AgentPolicy{Spec: input.Resolution.AgentPolicy}
	policy.UID = "example-policy-uid"
	requireFixedReason(t, ValidatePolicyConfiguration(policy, input.Resolution.Capabilities, input.Platform), ErrConflict,
		"control input budget exceeded")

	budget := networkRuleBudget{rules: MaxNetworkRules}
	requireFixedReason(t, budget.take(1), ErrConflict, fmt.Sprintf("network rule count exceeds %d", MaxNetworkRules))
	budget = networkRuleBudget{entries: MaxNetworkRuleEntries}
	requireFixedReason(t, budget.take(1), ErrConflict, fmt.Sprintf("expanded network rule entries exceed %d", MaxNetworkRuleEntries))

	oversized := CiliumNetworkPolicy{}
	oversized.Name = valueCanary
	oversized.Annotations = map[string]string{"example": strings.Repeat("a", MaxNetworkPolicyBytes)}
	requireFixedReason(t, validateNetworkPolicySize(oversized), ErrConflict,
		fmt.Sprintf("a generated network policy exceeds %d bytes", MaxNetworkPolicyBytes))
}
