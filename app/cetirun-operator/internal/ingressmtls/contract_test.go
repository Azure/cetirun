// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package ingressmtls

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestBootstrapKeepsExactTrustAndBoundedConnections(t *testing.T) {
	plan := Plan{Binding: Binding{Harness: "hermes", Port: 8642, Terminal: true, ClientURI: "spiffe://example.com/gateway/agent-uid"}}
	raw, err := plan.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	resources := document["static_resources"].(map[string]any)
	listeners := resources["listeners"].([]any)
	if len(listeners) != 3 || strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatal("endpoint did not cover all ingress surfaces or embedded a credential")
	}
	for _, rawListener := range listeners {
		listener := rawListener.(map[string]any)
		socket := listener["address"].(map[string]any)["socket_address"].(map[string]any)
		if socket["address"] != "::" || socket["ipv4_compat"] != true {
			t.Fatal("endpoint did not cover both registered address families")
		}
		chain := listener["filter_chains"].([]any)[0].(map[string]any)
		tls := chain["transport_socket"].(map[string]any)["typed_config"].(map[string]any)
		if tls["require_client_certificate"] != true || tls["disable_stateless_session_resumption"] != true ||
			tls["disable_stateful_session_resumption"] != true {
			t.Fatal("endpoint relaxed mandatory authentication or enabled reusable sessions")
		}
		context := tls["common_tls_context"].(map[string]any)
		validation := context["validation_context"].(map[string]any)
		san := validation["match_typed_subject_alt_names"].([]any)[0].(map[string]any)
		if san["san_type"] != "URI" || san["matcher"].(map[string]any)["exact"] != plan.ClientURI {
			t.Fatal("endpoint changed exact peer scope")
		}
		proxy := chain["filters"].([]any)[0].(map[string]any)["typed_config"].(map[string]any)
		if proxy["max_downstream_connection_duration"] != "300s" {
			t.Fatal("endpoint lost its bounded authenticated connection lifetime")
		}
	}

	for _, cluster := range resources["clusters"].([]any) {
		raw, _ := json.Marshal(cluster)
		if !strings.Contains(string(raw), `"address":"127.0.0.1"`) || strings.Contains(string(raw), `"address":"0.0.0.0"`) {
			t.Fatal("endpoint acquired a non-loopback backend")
		}
	}
}

func TestBindingPreservesHarnessPrimaryPorts(t *testing.T) {
	for _, tt := range []struct {
		harness string
		port    int32
		valid   bool
	}{
		{"openclaw", 9120, true},
		{"custom", 9120, true},
		{"openclaw", 9119, false},
		{"custom", 9119, false},
		{"hermes", 8642, true},
		{"hermes", 8080, true},
		{"hermes", 9119, false},
		{"hermes", 9120, false},
	} {
		binding := Binding{
			AgentUID: "agent-uid", Namespace: "agent-space", Harness: tt.harness, Port: tt.port,
			ServerSecret: Reference{Namespace: "agent-space", Name: "agent-tls", UID: "server-uid", ResourceVersion: "1"},
			ClientSecret: Reference{Namespace: "gateway-space", Name: "client-tls", UID: "client-uid", ResourceVersion: "1"},
			Gateway:      Reference{Namespace: "gateway-space", Name: "gateway", UID: "gateway-uid", ResourceVersion: "1"},
			ClientURI:    "spiffe://example.com/gateway/agent-uid",
		}
		if err := binding.Validate(); (err == nil) != tt.valid {
			t.Errorf("%s primary port %d: Validate() = %v, want valid=%t", tt.harness, tt.port, err, tt.valid)
		}
	}
}

func TestDesktopRegistrationPreservesDesktopDisabledApprovals(t *testing.T) {
	binding := Binding{
		AgentUID: "agent-uid", Namespace: "agent-space", Harness: "hermes", Port: 8642, Terminal: true,
		ServerSecret: Reference{Namespace: "agent-space", Name: "agent-tls", UID: "server-uid", ResourceVersion: "1"},
		ClientSecret: Reference{Namespace: "gateway-space", Name: "client-tls", UID: "client-uid", ResourceVersion: "1"},
		Gateway:      Reference{Namespace: "gateway-space", Name: "gateway", UID: "gateway-uid", ResourceVersion: "1"},
		ClientURI:    "spiffe://example.com/gateway/agent-uid",
	}
	raw, err := json.Marshal(binding)
	if err != nil || strings.Contains(string(raw), `"desktop"`) {
		t.Fatal("desktop-disabled approval serialization changed")
	}
	if err := json.Unmarshal(raw, &binding); err != nil {
		t.Fatal(err)
	}
	if len((Plan{Binding: binding}).Ports()) != 3 {
		t.Fatal("an older approval implicitly enabled desktop")
	}
	for _, port := range []int32{DesktopPort, 5900, 6080, 18800} {
		binding.Port = port
		if err := binding.Validate(); err != nil {
			t.Fatal("desktop-disabled approval gained a conflicting reserved port:", err)
		}
		binding.Desktop = true
		if err := binding.Validate(); err == nil {
			t.Fatal("desktop approval allowed a conflicting primary port:", port)
		}
		binding.Desktop = false
	}
	binding.Port, binding.Desktop = 8642, true
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
	ports := (Plan{Binding: binding}).Ports()
	if len(ports) != 4 || ports[3] != (Port{Name: "novnc", Backend: 6080, TLS: DesktopPort}) {
		t.Fatal("explicit desktop approval did not add its TLS-only surface")
	}
}

func TestReferenceVersionIsOptional(t *testing.T) {
	legacy := Reference{Namespace: "gateway-space", Name: "client-tls", UID: "client-uid", ResourceVersion: "7"}
	uidOnly := Reference{Namespace: "gateway-space", Name: "client-tls", UID: "client-uid"}
	for _, ref := range []Reference{legacy, uidOnly} {
		if err := ref.Validate(); err != nil {
			t.Fatal("valid reference refused:", err)
		}
	}
	raw, err := json.Marshal(uidOnly)
	if err != nil || strings.Contains(string(raw), "resourceVersion") {
		t.Fatal("UID-only reference serialized a version:", string(raw))
	}
	var decoded Reference
	if err := json.Unmarshal([]byte(`{"namespace":"gateway-space","name":"client-tls","uid":"client-uid"}`), &decoded); err != nil || decoded != uidOnly {
		t.Fatal("registration without resourceVersion did not decode as UID-only:", err)
	}
	for _, bad := range []Reference{
		{Namespace: "gateway-space", Name: "client-tls"},
		{Namespace: "gateway-space", Name: "client-tls", UID: "client-uid", ResourceVersion: " 7"},
	} {
		if bad.Validate() == nil {
			t.Fatal("invalid reference accepted:", bad)
		}
	}
	if !legacy.Matches("client-uid", "7") || legacy.Matches("client-uid", "8") || legacy.Matches("other", "7") {
		t.Fatal("version-pinned reference lost its strict match")
	}
	if !uidOnly.Matches("client-uid", "8") || uidOnly.Matches("other", "8") || uidOnly.Matches("", "") {
		t.Fatal("UID-only reference did not match by UID alone")
	}
}

func TestStaleApprovalNamesTheObject(t *testing.T) {
	err := Reference{Namespace: "gateway-space", Name: "client-tls", UID: "client-uid"}.Stale("Secret", "the object was replaced")
	if !errors.Is(fmt.Errorf("wrapped: %w", err), ErrApprovalStale) {
		t.Fatal("stale approval is not ErrApprovalStale")
	}
	var stale *StaleError
	if !errors.As(err, &stale) || stale.Kind != "Secret" || stale.Namespace != "gateway-space" || stale.Name != "client-tls" ||
		!strings.Contains(err.Error(), "Secret gateway-space/client-tls") {
		t.Fatal("stale approval does not name its object:", err)
	}
}
