// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"sigs.k8s.io/yaml"
)

type peerPolicyDocument struct {
	Kind string `json:"kind"`
	Spec struct {
		Variables []struct {
			Name       string `json:"name"`
			Expression string `json:"expression"`
		} `json:"variables"`
		Validations []struct {
			Expression string `json:"expression"`
		} `json:"validations"`
	} `json:"spec"`
}

// TestProtectedPeerLabelPolicy evaluates the rendered admission policy CEL.
// The API server binding and namespace selection are covered by the chart
// render tests; this test covers the decisions of the expressions.
func TestProtectedPeerLabelPolicy(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("requires Helm to render the operator chart")
	}
	command := exec.Command("helm", "template", "peer-guard", "../../chart", "--kube-version", "1.37.0",
		"--show-only", "templates/sandbox-peer-admission.yaml",
		"--set", "cetiadmin.enabled=false",
		"--set", "sandboxAdmission.enabled=true", "--set", "sandboxAdmission.enforce=true",
		"--set", "sandboxAdmission.podsEnabled=true", "--set", "sandboxAdmission.tlsSecretName=synthetic-tls",
		"--set-string", "sandboxAdmission.caBundle="+syntheticCABundle(t), "--set", "sandboxAdmission.adminUsers[0]=admin@example.com",
		"--set", "sandboxSources.enabled=true", "--set", "sandboxSources.nativeExecution=true",
		"--set", "sandboxSources.bootstrapOwnedPrerequisites=true",
		"--set", "sandboxSources.platformConfigMapName=sandbox-platform")
	command.Env = append(os.Environ(), "KUBECONFIG="+os.DevNull, "HELM_REGISTRY_CONFIG="+os.DevNull, "HELM_REPOSITORY_CONFIG="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("render the protected peer policy: %v\n%s", err, output)
	}
	var policy peerPolicyDocument
	for _, document := range strings.Split(string(output), "\n---") {
		var candidate peerPolicyDocument
		if yaml.Unmarshal([]byte(document), &candidate) == nil && candidate.Kind == "ValidatingAdmissionPolicy" {
			policy = candidate
		}
	}
	if len(policy.Spec.Validations) == 0 {
		t.Fatal("the protected peer policy was not rendered")
	}
	env, err := cel.NewEnv(cel.Variable("request", cel.DynType), cel.Variable("object", cel.DynType),
		cel.Variable("oldObject", cel.DynType), cel.Variable("variables", cel.DynType))
	if err != nil {
		t.Fatal(err)
	}
	evaluate := func(expression string, inputs map[string]any) any {
		t.Helper()
		ast, issues := env.Compile(expression)
		if issues.Err() != nil {
			t.Fatal(issues.Err())
		}
		program, err := env.Program(ast)
		if err != nil {
			t.Fatal(err)
		}
		value, _, err := program.Eval(inputs)
		if err != nil {
			t.Fatalf("evaluate %q: %v", expression, err)
		}
		return value.Value()
	}
	allowed := func(operation, username string, labels, oldLabels map[string]any) bool {
		t.Helper()
		pod := func(labels map[string]any) any {
			if labels == nil {
				return map[string]any{"metadata": map[string]any{}}
			}
			return map[string]any{"metadata": map[string]any{"labels": labels}}
		}
		inputs := map[string]any{
			"request": map[string]any{"operation": operation, "userInfo": map[string]any{"username": username}},
			"object":  pod(labels), "oldObject": types.NullValue, "variables": map[string]any{},
		}
		if operation == "UPDATE" {
			inputs["oldObject"] = pod(oldLabels)
		}
		variables := map[string]any{}
		for _, variable := range policy.Spec.Variables {
			inputs["variables"] = variables
			variables[variable.Name] = evaluate(variable.Expression, inputs)
		}
		inputs["variables"] = variables
		for _, validation := range policy.Spec.Validations {
			if evaluate(validation.Expression, inputs) != true {
				return false
			}
		}
		return true
	}
	const (
		replicaSet = "system:serviceaccount:kube-system:replicaset-controller"
		user       = "system:serviceaccount:cetirun-system:cetirun-portal"
		key        = "sandbox.cetirun.microsoft.com/peer"
	)
	protected := map[string]any{key: "ai-gateway", "app.kubernetes.io/component": "gateway"}
	plain := map[string]any{"app.kubernetes.io/component": "gateway"}
	for _, tc := range []struct {
		name               string
		operation, creator string
		labels, oldLabels  map[string]any
		want               bool
	}{
		{"ReplicaSet controller creates a Gateway Pod", "CREATE", replicaSet, protected, nil, true},
		{"controller manager creates a Gateway Pod", "CREATE", "system:kube-controller-manager", protected, nil, true},
		{"another identity creates a decoy Pod", "CREATE", user, protected, nil, false},
		{"another identity creates an ordinary Pod", "CREATE", user, plain, nil, true},
		{"another identity creates a Pod without labels", "CREATE", user, nil, nil, true},
		{"label added to an existing Pod", "UPDATE", user, protected, plain, false},
		{"label added by the ReplicaSet controller", "UPDATE", replicaSet, protected, plain, false},
		{"label value changed", "UPDATE", user, map[string]any{key: "other"}, protected, false},
		{"label removed", "UPDATE", user, plain, protected, false},
		{"label removed with all labels", "UPDATE", user, nil, protected, false},
		{"other labels changed", "UPDATE", user, map[string]any{key: "ai-gateway", "extra": "x"}, protected, true},
		{"ordinary Pod relabelled", "UPDATE", user, map[string]any{"extra": "x"}, plain, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := allowed(tc.operation, tc.creator, tc.labels, tc.oldLabels); got != tc.want {
				t.Fatalf("allowed = %t, want %t", got, tc.want)
			}
		})
	}
}

// syntheticCABundle returns an in-memory, short-lived CA certificate. Only
// chart validation reads it; no key leaves the test.
func syntheticCABundle(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "sandbox-admission.example.com"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
