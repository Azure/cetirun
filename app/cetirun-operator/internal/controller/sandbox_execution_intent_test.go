// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxwriter"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func executionIntentFixture(t *testing.T, typ string) (*preparationFixture, sandboxcoordination.EnrolledTarget) {
	t.Helper()
	f, node, _ := placementPreparationFixture(t, typ)
	if err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	preparedRevision(t, f)
	if err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	expected := f.expectation(t)
	identity := sandbox.ExecutionIdentity{
		Binding: expected.Binding, Revision: expected.Revision,
		PodUID: "independently-enrolled-pod", NodeUID: node.UID, EnforcementEpoch: "synthetic-enrolled-epoch",
	}
	return f, sandboxcoordination.EnrolledTarget{
		ExecutionIdentity: identity,
		Writer: sandboxwriter.Writer{Workload: expected.Workload,
			Pod:  w.ObjectIdentity{Name: expected.Workload.Name + "-0", UID: identity.PodUID},
			Node: expected.Placement.Nodes[0], MachineID: "synthetic-machine-incarnation", Epoch: identity.EnforcementEpoch,
			Storage: sandboxwriter.StorageIdentity{Volume: w.ObjectIdentity{Name: "example-pv", UID: "pv-uid"},
				Driver: "csi.example.com", Handle: "synthetic-volume-incarnation"},
		},
		WriterJournal: w.ObjectIdentity{
			Name: sandboxwriter.JournalName(sandboxwriter.Lifetime{Agent: expected.Agent, Namespace: expected.Namespace, Home: expected.Home}),
			UID:  "synthetic-enrolled-journal-uid",
		},
	}
}

func TestSandboxExecutionIntentPinsHeldNativePreparation(t *testing.T) {
	for _, typ := range []string{"openclaw", "hermes"} {
		t.Run(typ, func(t *testing.T) {
			f, target := executionIntentFixture(t, typ)
			before, stages := f.current(t), f.installer.stages
			expected := f.expectation(t)
			intent, err := f.reconciler.SandboxExecutionIntent(context.Background(), before, target)
			if err != nil {
				t.Fatal(err)
			}
			if intent.Expected != target.ExecutionIdentity || intent.Writer != target.Writer || intent.WriterJournal != target.WriterJournal ||
				intent.Agent.UID != before.UID || intent.AgentGeneration != before.Generation ||
				intent.Namespace != expected.Namespace || intent.Home != expected.Home || intent.Workload != expected.Workload ||
				!reflect.DeepEqual(intent.Placement, expected.Placement) || len(intent.Sources) != len(expected.Sources)+1 ||
				!slices.Contains(intent.Sources, intent.Preparation) {
				t.Fatalf("intent did not preserve the prepared source and identity tuple: %+v", intent)
			}
			after := f.current(t)
			if !reflect.DeepEqual(before, after) || f.installer.stages != stages || !f.expectation(t).ExecutionHeld {
				t.Fatal("reserving an intent changed activation, readiness or preparation")
			}
			if err := f.reconciler.CheckSandboxExecutionSources(context.Background(), intent); err != nil {
				t.Fatal("current pinned sources were not verifiable:", err)
			}
			state, err := sandboxcoordination.NewState("cetirun-system", "execution-coordination")
			if err != nil {
				t.Fatal(err)
			}
			if err := f.client.Create(context.Background(), state); err != nil {
				t.Fatal(err)
			}
			store := sandboxcoordination.Store{Client: f.client, APIReader: f.client, Namespace: state.Namespace,
				Identity: w.ObjectIdentity{Name: state.Name, UID: state.UID}}
			ticket, err := store.AcquireExecution(context.Background(), intent)
			if err != nil || ticket.Execution.Phase != sandboxcoordination.Reserved || ticket.Execution.Intent.Expected != target.ExecutionIdentity {
				t.Fatal("actual prepared intent could not enter serialized coordination:", err)
			}
			assertSandboxPrepared(t, f)
		})
	}
}

func TestSandboxExecutionIntentRejectsUnpreparedOrMismatchedTarget(t *testing.T) {
	for _, scenario := range []string{"missing ControllerRevision", "wrong Pod", "wrong Node", "wrong epoch", "wrong policy", "wrong revision", "missing placement"} {
		t.Run(scenario, func(t *testing.T) {
			f, target := executionIntentFixture(t, "hermes")
			switch scenario {
			case "missing ControllerRevision":
				expected := f.expectation(t)
				workload := &appsv1.StatefulSet{}
				if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: expected.Namespace.Name, Name: expected.Workload.Name}, workload); err != nil {
					t.Fatal(err)
				}
				workload.Status.UpdateRevision = ""
				if err := f.client.Status().Update(context.Background(), workload); err != nil {
					t.Fatal(err)
				}
			case "wrong Pod":
				target.PodUID = ""
			case "wrong Node":
				target.NodeUID = "foreign-node"
			case "wrong epoch":
				target.EnforcementEpoch = ""
			case "wrong policy":
				target.Binding.AgentPolicyUID = "foreign-policy"
			case "wrong revision":
				target.Revision = "foreign-revision"
			case "missing placement":
				f.reconciler.SandboxPreparation.Placement = nil
			}
			if intent, err := f.reconciler.SandboxExecutionIntent(context.Background(), f.current(t), target); err == nil ||
				!reflect.DeepEqual(intent, sandboxcoordination.Intent{}) {
				t.Fatalf("unprepared or unenrolled execution was accepted: %+v %v", intent, err)
			}
			if !f.expectation(t).ExecutionHeld {
				t.Fatal("rejection lost the activation hold")
			}
		})
	}
}

func TestSandboxExecutionSourcesRecheckSharedChangesWithoutRestaging(t *testing.T) {
	for _, scenario := range []string{"policy", "Team", "Secret metadata", "HOME", "Agent intent", "placement", "platform", "preparation"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			f, target := executionIntentFixture(t, "openclaw")
			intent, err := f.reconciler.SandboxExecutionIntent(ctx, f.current(t), target)
			if err != nil {
				t.Fatal(err)
			}
			stages := f.installer.stages
			if scenario == "platform" {
				platform := f.config.Platforms[api.SandboxKata]
				platform.Topology.Revision = "changed-topology"
				f.config.Platforms[api.SandboxKata] = platform
			} else {
				var key client.ObjectKey
				var object client.Object
				switch scenario {
				case "policy":
					object, key = &api.AgentPolicy{}, client.ObjectKey{Name: f.fixture.Policy.Name}
				case "Team":
					object, key = &api.Team{}, client.ObjectKey{Name: f.fixture.Team.Name}
				case "Secret metadata":
					object, key = &corev1.Secret{}, client.ObjectKey{Namespace: f.fixture.Namespace.Name, Name: f.fixture.Agent.Spec.GatewayTokenSecretRef}
				case "HOME":
					object, key = &corev1.PersistentVolumeClaim{}, client.ObjectKey{Namespace: intent.Namespace.Name, Name: intent.Home.Name}
				case "Agent intent":
					object, key = &api.Agent{}, client.ObjectKey{Name: intent.Agent.Name}
				case "placement":
					object, key = &corev1.ConfigMap{}, client.ObjectKey{Namespace: "cetirun-system", Name: intent.Placement.Registry.Name}
				case "preparation":
					object, key = &corev1.ConfigMap{}, client.ObjectKey{Namespace: intent.Preparation.Namespace, Name: intent.Preparation.Name}
				}
				if err := f.client.Get(ctx, key, object); err != nil {
					t.Fatal(err)
				}
				if agent, ok := object.(*api.Agent); ok {
					agent.Spec.Suspended = true
					agent.Generation++
				} else {
					object.SetLabels(map[string]string{"example.com/source-revision": "changed"})
				}
				if err := f.client.Update(ctx, object); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.reconciler.CheckSandboxExecutionSources(ctx, intent); err == nil {
				t.Fatal("stale execution sources remained current")
			}
			if f.installer.stages != stages {
				t.Fatal("source check restaged a workload instead of rejecting stale intent")
			}
		})
	}
}

func TestSandboxExecutionSourcesPermitStatusOnlyAgentUpdates(t *testing.T) {
	ctx := context.Background()
	f, target := executionIntentFixture(t, "hermes")
	intent, err := f.reconciler.SandboxExecutionIntent(ctx, f.current(t), target)
	if err != nil {
		t.Fatal(err)
	}
	agent := f.current(t)
	agent.Status.Conditions = append(agent.Status.Conditions, metav1.Condition{
		Type: "SyntheticStatus", Status: metav1.ConditionTrue, Reason: "StatusOnly",
		Message: "Synthetic non-authoritative test status", LastTransitionTime: metav1.Now(),
	})
	if err := f.client.Status().Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := f.reconciler.CheckSandboxExecutionSources(ctx, intent); err != nil {
		t.Fatal("status churn invalidated authorization-relevant Agent intent:", err)
	}
}
