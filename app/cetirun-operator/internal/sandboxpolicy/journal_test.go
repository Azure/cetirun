// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

type journalTestClient struct {
	client.Client
	creates, updates, deletes, reads int
	beforeCreate                     func(client.Object) error
	beforeUpdate                     func(client.Object) error
	beforeDelete                     func(client.Object, client.DeleteOptions) error
	afterCreate                      func(client.Object) error
	afterUpdate                      func(client.Object) error
}

func (c *journalTestClient) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	c.reads++
	return errors.New("synthetic cached client must not be read")
}

func (c *journalTestClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.creates++
	if c.beforeCreate != nil {
		if err := c.beforeCreate(obj); err != nil {
			return err
		}
	}
	if obj.GetUID() == "" {
		obj.SetUID(types.UID(fmt.Sprintf("synthetic-journal-%d", c.creates)))
	}
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	if c.afterCreate != nil {
		return c.afterCreate(obj)
	}
	return nil
}

func (c *journalTestClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.updates++
	if c.beforeUpdate != nil {
		if err := c.beforeUpdate(obj); err != nil {
			return err
		}
	}
	if err := c.Client.Update(ctx, obj, opts...); err != nil {
		return err
	}
	if c.afterUpdate != nil {
		return c.afterUpdate(obj)
	}
	return nil
}

func (c *journalTestClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.deletes++
	var options client.DeleteOptions
	for _, opt := range opts {
		opt.ApplyToDelete(&options)
	}
	if c.beforeDelete != nil {
		if err := c.beforeDelete(obj, options); err != nil {
			return err
		}
	}
	return c.Client.Delete(ctx, obj, opts...)
}

type journalTestReader struct {
	client.Reader
	get func(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error
}

func (r journalTestReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return r.get(ctx, key, obj, opts...)
}

func journalFixture(t *testing.T) (*Plan, GrantJournalStore, *journalTestClient) {
	t.Helper()
	plan, storage := revocationFixture(t)
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: plan.AgentName, UID: plan.Binding.AgentUID,
		Finalizers: []string{"example.com/agent-cleanup"},
	}}
	if err := storage.Create(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	writer := &journalTestClient{Client: storage}
	return plan, GrantJournalStore{Client: writer, APIReader: storage, Namespace: "operator-state"}, writer
}

func recordJournal(t *testing.T, store GrantJournalStore, plan *Plan, refs []BindingReference) {
	t.Helper()
	if err := store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, refs); err != nil {
		t.Fatal(err)
	}
}

func assertJournalInventory(t *testing.T, store GrantJournalStore, plan *Plan, want []BindingReference) {
	t.Helper()
	got, err := store.Read(context.Background(), plan.AgentName, plan.Binding.AgentUID)
	if err != nil || !slices.Equal(got, canonicalBindingReferences(want)) {
		t.Fatalf("journal inventory: got=%+v want=%+v error=%v", got, want, err)
	}
}

func readJournalCM(t *testing.T, store GrantJournalStore, storage client.Reader, plan *Plan) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{}
	if err := storage.Get(context.Background(), client.ObjectKey{
		Name: GrantJournalName(plan.Binding.AgentUID), Namespace: store.Namespace,
	}, cm); err != nil {
		t.Fatal(err)
	}
	return cm
}

func assertJournalProtection(t *testing.T, store GrantJournalStore, storage client.Reader, plan *Plan) {
	t.Helper()
	cm := readJournalCM(t, store, storage, plan)
	if !slices.Contains(cm.Finalizers, GrantJournalFinalizer) {
		t.Fatal("journal finalizer was removed")
	}
	assertJournalAgentUntouched(t, storage, plan)
}

func assertJournalAgentUntouched(t *testing.T, storage client.Reader, plan *Plan) {
	t.Helper()
	agent := &api.Agent{}
	if err := storage.Get(context.Background(), client.ObjectKey{Name: plan.AgentName}, agent); err != nil {
		t.Fatal("journal touched the Agent", err)
	}
	if agent.UID != plan.Binding.AgentUID || !agent.DeletionTimestamp.IsZero() ||
		!slices.Equal(agent.Finalizers, []string{"example.com/agent-cleanup"}) {
		t.Fatal("journal changed Agent identity, deletion state or finalizers")
	}
}

func journalBindingObject(t *testing.T, storage client.Reader, ref BindingReference) client.Object {
	t.Helper()
	var obj client.Object
	switch ref.Kind {
	case "RoleBinding":
		obj = &rbacv1.RoleBinding{}
	case "ClusterRoleBinding":
		obj = &rbacv1.ClusterRoleBinding{}
	default:
		t.Fatal("invalid test binding kind")
	}
	if err := storage.Get(context.Background(), client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func journalRaw(t *testing.T, plan *Plan, refs []BindingReference) string {
	t.Helper()
	raw, err := json.Marshal(grantJournalRecord{
		Version: 1, AgentName: plan.AgentName, AgentUID: plan.Binding.AgentUID, Bindings: refs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestGrantJournalRestartAndWriteAheadUnion(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	if _, err := store.Read(ctx, plan.AgentName, plan.Binding.AgentUID); !apierrors.IsNotFound(err) {
		t.Fatalf("missing journal is not NotFound: %v", err)
	}
	recordJournal(t, store, plan, nil)
	cm := readJournalCM(t, store, writer.Client, plan)
	if cm.Namespace != "operator-state" || cm.UID == "" || cm.ResourceVersion == "" ||
		!bindingOwned(cm, plan.AgentName, plan.Binding.AgentUID) ||
		cm.Annotations[AgentUIDLabel] != string(plan.Binding.AgentUID) ||
		!slices.Contains(cm.Finalizers, GrantJournalFinalizer) ||
		cm.OwnerReferences[0].BlockOwnerDeletion == nil || !*cm.OwnerReferences[0].BlockOwnerDeletion {
		t.Fatalf("incomplete durable empty journal metadata: %+v", cm.ObjectMeta)
	}
	if len(cm.Data) != 1 || len(cm.BinaryData) != 0 ||
		!strings.Contains(cm.Data[GrantJournalDataKey], `"bindings":[]`) {
		t.Fatal("empty desired did not create a typed, durable empty journal")
	}
	restarted := GrantJournalStore{Client: writer, APIReader: writer.Client, Namespace: store.Namespace}
	assertJournalInventory(t, restarted, plan, nil)
	recordJournal(t, store, plan, plan.Identity.Bindings[:1])
	recordJournal(t, restarted, plan, plan.Identity.Bindings[1:])
	recordJournal(t, store, plan, nil)
	assertJournalInventory(t, restarted, plan, plan.Identity.Bindings)
	read, err := restarted.Read(ctx, plan.AgentName, plan.Binding.AgentUID)
	if err != nil {
		t.Fatal(err)
	}
	read[0].Name = "caller-mutation"
	assertJournalInventory(t, restarted, plan, plan.Identity.Bindings)
	if writer.deletes != 0 || writer.reads != 0 {
		t.Fatal("recording inventory had binding effects or used the cached client")
	}
	assertJournalProtection(t, store, writer.Client, plan)
}

func TestGrantJournalRecordRequestCounts(t *testing.T) {
	plan, store, writer := journalFixture(t)
	desired := plan.Identity.Bindings
	known := slices.Clone(desired)
	for i := range known {
		known[i].UID = journalBindingObject(t, writer.Client, known[i]).GetUID()
	}
	partlyKnown := slices.Clone(desired)
	partlyKnown[0].UID = known[0].UID
	reordered := slices.Clone(known)
	slices.Reverse(reordered)
	changed := desired[0]
	changed.Name = "changed-grant"
	changed.GrantRevision = strings.Repeat("a", 64)
	merged := append(slices.Clone(known), changed)
	baseReader := store.APIReader
	reads := 0
	store.APIReader = journalTestReader{Reader: baseReader, get: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		reads++
		return baseReader.Get(ctx, key, obj, opts...)
	}}
	for _, tc := range []struct {
		name                    string
		desired, want           []BindingReference
		reads, creates, updates int
	}{
		{"initialize empty", nil, nil, 2, 1, 0},
		{"repeat empty", nil, nil, 1, 0, 0},
		{"explicit empty", []BindingReference{}, nil, 1, 0, 0},
		{"first binding", desired[:1], desired[:1], 2, 0, 1},
		{"additional bindings", desired[1:], desired, 2, 0, 1},
		{"repeat write-ahead inventory", desired, desired, 1, 0, 0},
		{"first UID", known[:1], partlyKnown, 2, 0, 1},
		{"remaining UIDs", known[1:], known, 2, 0, 1},
		{"repeat known inventory", known, known, 1, 0, 0},
		{"repeat again", known, known, 1, 0, 0},
		{"reordered", reordered, known, 1, 0, 0},
		{"subset", known[:1], known, 1, 0, 0},
		{"empty UIDs", desired, known, 1, 0, 0},
		{"subset with empty UIDs", desired[1:], known, 1, 0, 0},
		{"nil desired", nil, known, 1, 0, 0},
		{"empty desired", []BindingReference{}, known, 1, 0, 0},
		{"changed inventory", []BindingReference{changed}, merged, 2, 0, 1},
		{"repeat changed inventory retains obsolete bindings", []BindingReference{changed}, merged, 1, 0, 0},
	} {
		if !t.Run(tc.name, func(t *testing.T) {
			beforeReads, beforeCreates, beforeUpdates := reads, writer.creates, writer.updates
			var before *corev1.ConfigMap
			if tc.creates == 0 && tc.updates == 0 {
				before = readJournalCM(t, store, writer.Client, plan)
			}
			recordJournal(t, store, plan, tc.desired)
			if reads-beforeReads != tc.reads || writer.creates-beforeCreates != tc.creates ||
				writer.updates-beforeUpdates != tc.updates || writer.deletes != 0 || writer.reads != 0 {
				t.Fatalf("Record requests: gets=%d creates=%d updates=%d deletes=%d cached reads=%d; want gets=%d creates=%d updates=%d and no other effects",
					reads-beforeReads, writer.creates-beforeCreates, writer.updates-beforeUpdates, writer.deletes, writer.reads,
					tc.reads, tc.creates, tc.updates)
			}
			if before != nil && !reflect.DeepEqual(before, readJournalCM(t, store, writer.Client, plan)) {
				t.Fatal("unchanged inventory modified the journal")
			}
			assertJournalInventory(t, store, plan, tc.want)
			assertJournalProtection(t, store, writer.Client, plan)
		}) {
			return
		}
	}
}

func TestGrantJournalRecordCanonicalizesStoredPayload(t *testing.T) {
	plan, store, writer := journalFixture(t)
	recordJournal(t, store, plan, plan.Identity.Bindings)
	cm := readJournalCM(t, store, writer.Client, plan)
	canonical := cm.Data[GrantJournalDataKey]
	cm.Data[GrantJournalDataKey] = "\n " + canonical + "\n"
	if err := writer.Client.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	baseReader := store.APIReader
	reads := 0
	store.APIReader = journalTestReader{Reader: baseReader, get: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		reads++
		return baseReader.Get(ctx, key, obj, opts...)
	}}
	recordJournal(t, store, plan, plan.Identity.Bindings)
	if reads != 2 || writer.creates != 1 || writer.updates != 1 || writer.deletes != 0 || writer.reads != 0 {
		t.Fatalf("canonicalization requests: gets=%d creates=%d updates=%d deletes=%d cached reads=%d",
			reads, writer.creates, writer.updates, writer.deletes, writer.reads)
	}
	if got := readJournalCM(t, store, writer.Client, plan).Data[GrantJournalDataKey]; got != canonical {
		t.Fatalf("stored payload is not canonical: %q", got)
	}
	assertJournalInventory(t, store, plan, plan.Identity.Bindings)
	assertJournalProtection(t, store, writer.Client, plan)
}

func TestGrantJournalNameIsStableUIDBoundAndDNSValid(t *testing.T) {
	for _, uid := range []types.UID{"example-agent-uid", "replacement-agent-uid", "a"} {
		name := GrantJournalName(uid)
		if name != GrantJournalName(uid) || len(name) > 63 || len(validation.IsDNS1123Subdomain(name)) != 0 {
			t.Fatalf("invalid or unstable journal name: %q", name)
		}
		if uid != "replacement-agent-uid" && name == GrantJournalName("replacement-agent-uid") {
			t.Fatal("replaced Agents alias a journal")
		}
	}
}

func TestGrantJournalKnownUIDCannotBeWeakened(t *testing.T) {
	plan, store, writer := journalFixture(t)
	recordJournal(t, store, plan, plan.Identity.Bindings)
	known := slices.Clone(plan.Identity.Bindings)
	for i := range known {
		known[i].UID = journalBindingObject(t, writer.Client, known[i]).GetUID()
	}
	recordJournal(t, store, plan, known)
	recordJournal(t, store, plan, plan.Identity.Bindings)
	assertJournalInventory(t, store, plan, known)

	ref := known[0]
	previous := journalBindingObject(t, writer.Client, ref)
	if err := writer.Client.Delete(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	replacement := previous.DeepCopyObject().(client.Object)
	replacement.SetUID("replacement-binding")
	replacement.SetResourceVersion("")
	if err := writer.Client.Create(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(context.Background(), plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings[:1]); !errors.Is(err, ErrOwnership) {
		t.Fatalf("empty-UID removal ignored the durable known UID: %v", err)
	}
	if writer.deletes != 0 {
		t.Fatal("a replacement binding was deleted")
	}
	assertJournalInventory(t, store, plan, known)
	assertJournalProtection(t, store, writer.Client, plan)
}

func TestGrantJournalRejectsImmutableIdentityDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*BindingReference)
	}{
		{"binding UID", func(r *BindingReference) { r.UID = "replacement-binding" }},
		{"role name", func(r *BindingReference) { r.RoleRef.Name = "another-role" }},
		{"role kind", func(r *BindingReference) { r.RoleRef.Kind = "Role" }},
		{"subject name", func(r *BindingReference) { r.ServiceAccountName = "another-account" }},
		{"subject namespace", func(r *BindingReference) { r.ServiceAccountNamespace = "another-namespace" }},
		{"grant revision", func(r *BindingReference) { r.GrantRevision = strings.Repeat("a", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, store, writer := journalFixture(t)
			ref := plan.Identity.Bindings[0]
			for _, candidate := range plan.Identity.Bindings {
				if candidate.Kind == "RoleBinding" {
					ref = candidate
				}
			}
			ref.UID = "known-binding"
			recordJournal(t, store, plan, []BindingReference{ref})
			changed := ref
			tc.mutate(&changed)
			updates := writer.updates
			if err := store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, []BindingReference{changed}); !errors.Is(err, ErrConflict) {
				t.Fatalf("immutable journal identity drift accepted: %v", err)
			}
			if err := store.Revoke(context.Background(), plan.AgentName, plan.Binding.AgentUID, []BindingReference{changed}); !errors.Is(err, ErrOwnership) {
				t.Fatalf("unrecorded changed identity was revoked: %v", err)
			}
			if writer.updates != updates || writer.deletes != 0 {
				t.Fatal("identity conflict caused a write")
			}
			assertJournalInventory(t, store, plan, []BindingReference{ref})
			assertJournalProtection(t, store, writer.Client, plan)
		})
	}
}

func TestGrantJournalValidatesAllIncomingReferencesBeforeEffects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*BindingReference)
	}{
		{"Agent name", func(r *BindingReference) { r.AgentName = "another-agent" }},
		{"Agent UID", func(r *BindingReference) { r.AgentUID = "another-agent-uid" }},
		{"empty binding name", func(r *BindingReference) { r.Name = "" }},
		{"invalid binding name", func(r *BindingReference) { r.Name = "bad/name" }},
		{"invalid binding UID", func(r *BindingReference) { r.UID = "bad/uid" }},
		{"oversized binding UID", func(r *BindingReference) { r.UID = types.UID(strings.Repeat("a", 64)) }},
		{"nonbinding kind", func(r *BindingReference) { r.Kind = "ClusterRole" }},
		{"empty binding namespace", func(r *BindingReference) { r.Kind, r.Namespace = "RoleBinding", "" }},
		{"invalid binding namespace", func(r *BindingReference) { r.Kind, r.Namespace = "RoleBinding", "bad.namespace" }},
		{"namespaced cluster binding", func(r *BindingReference) { r.Kind, r.Namespace = "ClusterRoleBinding", "operations" }},
		{"cluster binding local role", func(r *BindingReference) {
			r.Kind, r.Namespace, r.RoleRef.Kind = "ClusterRoleBinding", "", "Role"
		}},
		{"empty account", func(r *BindingReference) { r.ServiceAccountName = "" }},
		{"invalid account", func(r *BindingReference) { r.ServiceAccountName = "bad/account" }},
		{"empty subject namespace", func(r *BindingReference) { r.ServiceAccountNamespace = "" }},
		{"invalid subject namespace", func(r *BindingReference) { r.ServiceAccountNamespace = "bad.namespace" }},
		{"role API group", func(r *BindingReference) { r.RoleRef.APIGroup = "" }},
		{"role kind", func(r *BindingReference) { r.RoleRef.Kind = "ConfigMap" }},
		{"empty role", func(r *BindingReference) { r.RoleRef.Name = "" }},
		{"dot role", func(r *BindingReference) { r.RoleRef.Name = "." }},
		{"dotdot role", func(r *BindingReference) { r.RoleRef.Name = ".." }},
		{"invalid role", func(r *BindingReference) { r.RoleRef.Name = "bad/role" }},
		{"escaped role", func(r *BindingReference) { r.RoleRef.Name = "bad%2frole" }},
		{"oversized role", func(r *BindingReference) { r.RoleRef.Name = strings.Repeat("a", 254) }},
		{"invalid UTF-8 role", func(r *BindingReference) { r.RoleRef.Name = "\xff" }},
		{"empty revision", func(r *BindingReference) { r.GrantRevision = "" }},
		{"opaque revision", func(r *BindingReference) { r.GrantRevision = "not-a-grant-digest" }},
		{"uppercase revision", func(r *BindingReference) { r.GrantRevision = strings.Repeat("A", 64) }},
		{"invalid digest", func(r *BindingReference) { r.GrantRevision = strings.Repeat("z", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, store, writer := journalFixture(t)
			recordJournal(t, store, plan, plan.Identity.Bindings)
			refs := slices.Clone(plan.Identity.Bindings)
			tc.mutate(&refs[1])
			updates := writer.updates
			if err := store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, refs); err == nil {
				t.Fatal("malformed desired reference accepted")
			}
			if err := store.Revoke(context.Background(), plan.AgentName, plan.Binding.AgentUID, refs); err == nil {
				t.Fatal("malformed removal request accepted")
			}
			if writer.updates != updates || writer.deletes != 0 {
				t.Fatal("input validation happened after a binding effect")
			}
			assertJournalInventory(t, store, plan, plan.Identity.Bindings)
			assertJournalProtection(t, store, writer.Client, plan)
		})
	}
}

func TestGrantJournalRejectsDuplicateIncomingIdentities(t *testing.T) {
	for _, conflicting := range []bool{false, true} {
		t.Run(fmt.Sprint(conflicting), func(t *testing.T) {
			plan, store, writer := journalFixture(t)
			ref := plan.Identity.Bindings[0]
			refs := []BindingReference{ref, ref}
			if conflicting {
				refs[1].ServiceAccountName = "different-account"
			}
			if err := store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, refs); !errors.Is(err, ErrConflict) {
				t.Fatalf("duplicate desired identity accepted: %v", err)
			}
			if writer.creates != 0 {
				t.Fatal("duplicate input created a journal")
			}
			recordJournal(t, store, plan, []BindingReference{ref})
			if err := store.Revoke(context.Background(), plan.AgentName, plan.Binding.AgentUID, refs); !errors.Is(err, ErrConflict) {
				t.Fatalf("duplicate removal identity accepted: %v", err)
			}
			if writer.deletes != 0 {
				t.Fatal("duplicate removal affected a binding")
			}
			assertJournalProtection(t, store, writer.Client, plan)
		})
	}
}

func TestGrantJournalAcceptsNamespacedRolesAndSystemClusterRoles(t *testing.T) {
	plan, store, _ := journalFixture(t)
	refs := slices.Clone(plan.Identity.Bindings)
	for i := range refs {
		if refs[i].Kind == "RoleBinding" {
			refs[i].RoleRef.Kind = "Role"
			refs[i].RoleRef.Name = "namespaced-reader"
		} else {
			refs[i].RoleRef.Name = "system:discovery"
		}
	}
	recordJournal(t, store, plan, refs)
	assertJournalInventory(t, store, plan, refs)
}

func TestGrantJournalAdmissionSourceRoleAndCrossNamespaceSubject(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	ref := BindingReference{
		Kind: "RoleBinding", Namespace: plan.Namespace, Name: "sandbox-admission-source-read",
		AgentName: plan.AgentName, AgentUID: plan.Binding.AgentUID,
		ServiceAccountName: "sandbox-admission", ServiceAccountNamespace: store.Namespace,
		RoleRef:       rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "sandbox-admission-source-read"},
		GrantRevision: strings.Repeat("b", 64),
	}
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: ref.RoleRef.Name, Namespace: ref.Namespace, UID: "source-read-role"},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"configmaps"},
			ResourceNames: []string{"sandbox-source"}, Verbs: []string{"get"},
		}},
	}
	account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: ref.ServiceAccountName, Namespace: ref.ServiceAccountNamespace, UID: "operator-admission-account",
	}}
	for _, obj := range []client.Object{role, account} {
		if err := writer.Client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	recordJournal(t, store, plan, plan.Identity.Bindings)
	recordJournal(t, store, plan, []BindingReference{ref})
	controller, block := true, true
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: ref.Name, Namespace: ref.Namespace, UID: "admission-source-binding",
			Labels:      map[string]string{AgentUIDLabel: string(ref.AgentUID)},
			Annotations: map[string]string{GrantRevisionAnnotation: ref.GrantRevision},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: ref.AgentName,
				UID: ref.AgentUID, Controller: &controller, BlockOwnerDeletion: &block,
			}},
		},
		RoleRef: ref.RoleRef,
		Subjects: []rbacv1.Subject{{
			Kind: "ServiceAccount", Name: ref.ServiceAccountName, Namespace: ref.ServiceAccountNamespace,
		}},
	}
	if err := writer.Client.Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	known := ref
	known.UID = binding.UID
	recordJournal(t, store, plan, []BindingReference{known})
	restarted := GrantJournalStore{Client: writer, APIReader: writer.Client, Namespace: store.Namespace}
	assertJournalInventory(t, restarted, plan, append(slices.Clone(plan.Identity.Bindings), known))
	if err := restarted.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, []BindingReference{ref}); err != nil {
		t.Fatal("admission RoleBinding with cross-namespace subject could not be revoked", err)
	}
	if err := writer.Client.Get(ctx, client.ObjectKeyFromObject(binding), &rbacv1.RoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatal("admission source-read binding still exists", err)
	}
	assertJournalInventory(t, restarted, plan, plan.Identity.Bindings)
	for _, workloadBinding := range plan.Identity.Bindings {
		journalBindingObject(t, writer.Client, workloadBinding)
	}
	for _, obj := range []client.Object{role, account} {
		if err := writer.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			t.Fatal("admission grant cleanup touched its Role or operator ServiceAccount", err)
		}
	}
	assertJournalProtection(t, restarted, writer.Client, plan)
}

func TestGrantJournalRejectsCorruptOrForeignStateBeforeEffects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*corev1.ConfigMap)
	}{
		{"wrong name", func(cm *corev1.ConfigMap) { cm.Name = "another-journal" }},
		{"wrong namespace", func(cm *corev1.ConfigMap) { cm.Namespace = "another-state-namespace" }},
		{"missing object UID", func(cm *corev1.ConfigMap) { cm.UID = "" }},
		{"missing resource version", func(cm *corev1.ConfigMap) { cm.ResourceVersion = "" }},
		{"wrong object kind", func(cm *corev1.ConfigMap) { cm.Kind = "Secret" }},
		{"wrong object API", func(cm *corev1.ConfigMap) { cm.APIVersion = "example.com/v1" }},
		{"missing owner", func(cm *corev1.ConfigMap) { cm.OwnerReferences = nil }},
		{"extra owner", func(cm *corev1.ConfigMap) {
			cm.OwnerReferences = append(cm.OwnerReferences, metav1.OwnerReference{UID: "another-owner"})
		}},
		{"wrong owner API", func(cm *corev1.ConfigMap) { cm.OwnerReferences[0].APIVersion = "example.com/v1" }},
		{"wrong owner kind", func(cm *corev1.ConfigMap) { cm.OwnerReferences[0].Kind = "Team" }},
		{"wrong owner name", func(cm *corev1.ConfigMap) { cm.OwnerReferences[0].Name = "another-agent" }},
		{"replaced owner", func(cm *corev1.ConfigMap) { cm.OwnerReferences[0].UID = "replacement-agent" }},
		{"missing controller", func(cm *corev1.ConfigMap) { cm.OwnerReferences[0].Controller = nil }},
		{"noncontroller owner", func(cm *corev1.ConfigMap) { *cm.OwnerReferences[0].Controller = false }},
		{"missing block owner deletion", func(cm *corev1.ConfigMap) { cm.OwnerReferences[0].BlockOwnerDeletion = nil }},
		{"nonblocking owner deletion", func(cm *corev1.ConfigMap) { *cm.OwnerReferences[0].BlockOwnerDeletion = false }},
		{"missing UID label", func(cm *corev1.ConfigMap) { delete(cm.Labels, AgentUIDLabel) }},
		{"foreign UID label", func(cm *corev1.ConfigMap) { cm.Labels[AgentUIDLabel] = "another-agent" }},
		{"missing UID annotation", func(cm *corev1.ConfigMap) { delete(cm.Annotations, AgentUIDLabel) }},
		{"foreign UID annotation", func(cm *corev1.ConfigMap) { cm.Annotations[AgentUIDLabel] = "another-agent" }},
		{"missing finalizer", func(cm *corev1.ConfigMap) { cm.Finalizers = nil }},
		{"duplicate finalizer", func(cm *corev1.ConfigMap) { cm.Finalizers = append(cm.Finalizers, GrantJournalFinalizer) }},
		{"missing data", func(cm *corev1.ConfigMap) { cm.Data = nil }},
		{"unknown data key", func(cm *corev1.ConfigMap) { cm.Data["unexpected"] = "synthetic" }},
		{"wrong data key", func(cm *corev1.ConfigMap) { cm.Data = map[string]string{"unexpected": "{}"} }},
		{"binary data", func(cm *corev1.ConfigMap) { cm.BinaryData = map[string][]byte{"unexpected": {1}} }},
		{"immutable map", func(cm *corev1.ConfigMap) { immutable := true; cm.Immutable = &immutable }},
		{"invalid JSON", func(cm *corev1.ConfigMap) { cm.Data[GrantJournalDataKey] = "{" }},
		{"multiple JSON documents", func(cm *corev1.ConfigMap) { cm.Data[GrantJournalDataKey] += "{}" }},
		{"invalid UTF-8", func(cm *corev1.ConfigMap) { cm.Data[GrantJournalDataKey] += "\xff" }},
		{"root null", func(cm *corev1.ConfigMap) { cm.Data[GrantJournalDataKey] = "null" }},
		{"missing bindings", func(cm *corev1.ConfigMap) { cm.Data[GrantJournalDataKey] = `{"version":1}` }},
		{"unknown envelope field", func(cm *corev1.ConfigMap) {
			cm.Data[GrantJournalDataKey] = strings.Replace(cm.Data[GrantJournalDataKey], `"version":1`, `"version":1,"unexpected":true`, 1)
		}},
		{"duplicate envelope field", func(cm *corev1.ConfigMap) {
			cm.Data[GrantJournalDataKey] = strings.Replace(cm.Data[GrantJournalDataKey], `"version":1`, `"version":1,"version":1`, 1)
		}},
		{"unknown version", func(cm *corev1.ConfigMap) {
			cm.Data[GrantJournalDataKey] = strings.Replace(cm.Data[GrantJournalDataKey], `"version":1`, `"version":2`, 1)
		}},
		{"case folded field", func(cm *corev1.ConfigMap) {
			cm.Data[GrantJournalDataKey] = strings.Replace(cm.Data[GrantJournalDataKey], `"version":1`, `"Version":1`, 1)
		}},
		{"unknown reference field", func(cm *corev1.ConfigMap) {
			cm.Data[GrantJournalDataKey] = strings.Replace(cm.Data[GrantJournalDataKey], `"roleRef":{`, `"unexpected":true,"roleRef":{`, 1)
		}},
		{"unknown role field", func(cm *corev1.ConfigMap) {
			cm.Data[GrantJournalDataKey] = strings.Replace(cm.Data[GrantJournalDataKey], `"roleRef":{`, `"roleRef":{"unexpected":true,`, 1)
		}},
		{"duplicate reference field", func(cm *corev1.ConfigMap) {
			cm.Data[GrantJournalDataKey] = strings.Replace(cm.Data[GrantJournalDataKey], `"roleRef":{`, `"roleRef":{},"roleRef":{`, 1)
		}},
		{"duplicate role field", func(cm *corev1.ConfigMap) {
			cm.Data[GrantJournalDataKey] = strings.Replace(cm.Data[GrantJournalDataKey], `"roleRef":{`, `"roleRef":{"kind":"ClusterRole",`, 1)
		}},
		{"escaped duplicate role field", func(cm *corev1.ConfigMap) {
			cm.Data[GrantJournalDataKey] = strings.Replace(cm.Data[GrantJournalDataKey], `"roleRef":{`, `"roleRef":{"ki\u006ed":"ClusterRole",`, 1)
		}},
		{"null optional UID", func(cm *corev1.ConfigMap) {
			cm.Data[GrantJournalDataKey] = strings.Replace(cm.Data[GrantJournalDataKey], `"roleRef":{`, `"uid":null,"roleRef":{`, 1)
		}},
		{"excess JSON nesting", func(cm *corev1.ConfigMap) {
			cm.Data[GrantJournalDataKey] = `{"bindings":[{"roleRef":{"unexpected":{}}}]}`
		}},
		{"oversized raw state", func(cm *corev1.ConfigMap) { cm.Data[GrantJournalDataKey] = strings.Repeat(" ", MaxGrantJournalBytes+1) }},
		{"oversized complete state", func(cm *corev1.ConfigMap) {
			cm.Annotations["example.com/metadata"] = strings.Repeat("a", MaxGrantJournalBytes)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, store, writer := journalFixture(t)
			recordJournal(t, store, plan, plan.Identity.Bindings)
			baseReader := store.APIReader
			reads := 0
			store.APIReader = journalTestReader{Reader: baseReader, get: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				reads++
				if err := baseReader.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				if cm, ok := obj.(*corev1.ConfigMap); ok {
					tc.mutate(cm)
				}
				return nil
			}}
			updates := writer.updates
			if _, err := store.Read(context.Background(), plan.AgentName, plan.Binding.AgentUID); err == nil {
				t.Fatal("corrupt journal was read as valid inventory")
			}
			if reads != 1 {
				t.Fatalf("corrupt journal Read used %d authoritative reads, want 1", reads)
			}
			for _, operation := range []func() error{
				func() error { return store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, nil) },
				func() error {
					return store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings)
				},
				func() error {
					return store.Revoke(context.Background(), plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings)
				},
				func() error { return store.Delete(context.Background(), plan.AgentName, plan.Binding.AgentUID) },
			} {
				beforeReads := reads
				if err := operation(); err == nil {
					t.Fatal("operation accepted corrupt journal state")
				}
				if reads-beforeReads != 1 {
					t.Fatalf("corrupt journal operation used %d authoritative reads, want 1", reads-beforeReads)
				}
			}
			if writer.creates != 1 || writer.updates != updates || writer.deletes != 0 || writer.reads != 0 {
				t.Fatal("corrupt journal caused a write or binding effect")
			}
			assertJournalProtection(t, store, writer.Client, plan)
		})
	}
}

func TestGrantJournalRejectsMalformedPersistedInventory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*grantJournalRecord)
	}{
		{"foreign record Agent", func(r *grantJournalRecord) { r.AgentName = "another-agent" }},
		{"foreign record UID", func(r *grantJournalRecord) { r.AgentUID = "another-agent-uid" }},
		{"missing inventory", func(r *grantJournalRecord) { r.Bindings = nil }},
		{"foreign reference Agent", func(r *grantJournalRecord) { r.Bindings[1].AgentName = "another-agent" }},
		{"foreign reference UID", func(r *grantJournalRecord) { r.Bindings[1].AgentUID = "another-agent-uid" }},
		{"duplicate reference", func(r *grantJournalRecord) { r.Bindings = append(r.Bindings, r.Bindings[0]) }},
		{"conflicting reference", func(r *grantJournalRecord) {
			other := r.Bindings[0]
			other.UID = "conflicting-binding"
			r.Bindings = append(r.Bindings, other)
		}},
		{"malformed final reference", func(r *grantJournalRecord) { r.Bindings[1].RoleRef.Name = "bad/role" }},
		{"excess references", func(r *grantJournalRecord) {
			r.Bindings = make([]BindingReference, MaxGrantJournalReferences+1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, store, writer := journalFixture(t)
			recordJournal(t, store, plan, plan.Identity.Bindings)
			cm := readJournalCM(t, store, writer.Client, plan)
			var record grantJournalRecord
			if err := json.Unmarshal([]byte(cm.Data[GrantJournalDataKey]), &record); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&record)
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			cm.Data[GrantJournalDataKey] = string(raw)
			if err := writer.Client.Update(context.Background(), cm); err != nil {
				t.Fatal(err)
			}
			if err := store.Revoke(context.Background(), plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); err == nil {
				t.Fatal("malformed persisted inventory accepted")
			}
			if writer.deletes != 0 || writer.updates != 0 {
				t.Fatal("persisted inventory validation occurred after binding effects")
			}
			assertJournalProtection(t, store, writer.Client, plan)
		})
	}
}

func TestGrantJournalRequiresExplicitNamespaceIdentityAndReaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*GrantJournalStore, *string, *types.UID)
	}{
		{"missing reader", func(s *GrantJournalStore, _ *string, _ *types.UID) { s.APIReader = nil }},
		{"empty namespace", func(s *GrantJournalStore, _ *string, _ *types.UID) { s.Namespace = "" }},
		{"invalid namespace", func(s *GrantJournalStore, _ *string, _ *types.UID) { s.Namespace = "bad.namespace" }},
		{"oversized namespace", func(s *GrantJournalStore, _ *string, _ *types.UID) { s.Namespace = strings.Repeat("a", 64) }},
		{"empty Agent name", func(_ *GrantJournalStore, name *string, _ *types.UID) { *name = "" }},
		{"invalid Agent name", func(_ *GrantJournalStore, name *string, _ *types.UID) { *name = "bad/name" }},
		{"missing UID", func(_ *GrantJournalStore, _ *string, uid *types.UID) { *uid = "" }},
		{"invalid UID", func(_ *GrantJournalStore, _ *string, uid *types.UID) { *uid = "bad/uid" }},
		{"oversized UID", func(_ *GrantJournalStore, _ *string, uid *types.UID) { *uid = types.UID(strings.Repeat("a", 64)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, store, writer := journalFixture(t)
			name, uid := plan.AgentName, plan.Binding.AgentUID
			tc.mutate(&store, &name, &uid)
			if _, err := store.Read(context.Background(), name, uid); err == nil || apierrors.IsNotFound(err) {
				t.Fatal("invalid store configuration accepted by Read", err)
			}
			for _, operation := range []func() error{
				func() error { return store.Record(context.Background(), name, uid, nil) },
				func() error { return store.Revoke(context.Background(), name, uid, nil) },
				func() error { return store.Delete(context.Background(), name, uid) },
			} {
				if err := operation(); err == nil {
					t.Fatal("invalid store configuration accepted")
				}
			}
			if writer.creates+writer.updates+writer.deletes+writer.reads != 0 {
				t.Fatal("invalid store performed effects or fell back to a cached read")
			}
		})
	}
	plan, store, _ := journalFixture(t)
	recordJournal(t, store, plan, nil)
	store.Client = nil
	assertJournalInventory(t, store, plan, nil)
	for _, operation := range []func() error{
		func() error { return store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, nil) },
		func() error { return store.Revoke(context.Background(), plan.AgentName, plan.Binding.AgentUID, nil) },
		func() error { return store.Delete(context.Background(), plan.AgentName, plan.Binding.AgentUID) },
	} {
		if err := operation(); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("missing writer accepted: %v", err)
		}
	}
}

func TestGrantJournalReferenceAndByteBounds(t *testing.T) {
	makeRefs := func(plan *Plan, count int) []BindingReference {
		refs := make([]BindingReference, count)
		for i := range refs {
			refs[i] = BindingReference{
				Kind: "RoleBinding", Namespace: "ns", Name: fmt.Sprintf("b-%04d", i),
				AgentName: plan.AgentName, AgentUID: plan.Binding.AgentUID,
				ServiceAccountName: "sa", ServiceAccountNamespace: "ns",
				RoleRef:       rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "view"},
				GrantRevision: strings.Repeat("a", 64),
			}
		}
		return refs
	}
	t.Run("reference limit and union overflow", func(t *testing.T) {
		plan, store, writer := journalFixture(t)
		refs := makeRefs(plan, MaxGrantJournalReferences+1)
		if err := store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, refs); !errors.Is(err, ErrConflict) {
			t.Fatalf("oversized input accepted: %v", err)
		}
		if writer.creates != 0 {
			t.Fatal("oversized input created a journal")
		}
		recordJournal(t, store, plan, refs[:MaxGrantJournalReferences])
		assertJournalInventory(t, store, plan, refs[:MaxGrantJournalReferences])
		if err := store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, refs[MaxGrantJournalReferences:]); !errors.Is(err, ErrConflict) {
			t.Fatalf("oversized union accepted: %v", err)
		}
		if writer.updates != 0 {
			t.Fatal("oversized union was persisted")
		}
		assertJournalProtection(t, store, writer.Client, plan)
	})
	t.Run("complete serialized size", func(t *testing.T) {
		plan, store, writer := journalFixture(t)
		refs := makeRefs(plan, 600)
		for i := range refs {
			refs[i].Name += strings.Repeat("a", 240)
			refs[i].RoleRef.Name = strings.Repeat("r", 253)
			refs[i].ServiceAccountName = strings.Repeat("s", 253)
		}
		if err := store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, refs); !errors.Is(err, ErrConflict) {
			t.Fatalf("byte budget not enforced: %v", err)
		}
		if writer.creates != 0 {
			t.Fatal("oversized journal was created")
		}
		recordJournal(t, store, plan, refs[:300])
		updates := writer.updates
		if err := store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, refs[300:]); !errors.Is(err, ErrConflict) {
			t.Fatalf("union byte budget not enforced: %v", err)
		}
		if writer.updates != updates {
			t.Fatal("oversized union was written")
		}
		assertJournalInventory(t, store, plan, refs[:300])
	})
	t.Run("exact serialized boundary includes metadata", func(t *testing.T) {
		plan, store, writer := journalFixture(t)
		recordJournal(t, store, plan, nil)
		cm := readJournalCM(t, store, writer.Client, plan)
		cm.Annotations["example.com/padding"] = ""
		raw, err := json.Marshal(cm)
		if err != nil {
			t.Fatal(err)
		}
		cm.Annotations["example.com/padding"] = strings.Repeat("a", MaxGrantJournalBytes-len(raw))
		if _, err := store.decode(cm, plan.AgentName, plan.Binding.AgentUID, true); err != nil {
			t.Fatal("exact byte limit was rejected", err)
		}
		cm.Annotations["example.com/padding"] += "a"
		if _, err := store.decode(cm, plan.AgentName, plan.Binding.AgentUID, true); !errors.Is(err, ErrConflict) {
			t.Fatal("one byte beyond limit was accepted", err)
		}
	})
}

func TestGrantJournalMissingStateCannotAuthorizeRevocation(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); !apierrors.IsNotFound(err) {
		t.Fatalf("missing durable inventory was treated as cleanup evidence: %v", err)
	}
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, nil); !apierrors.IsNotFound(err) {
		t.Fatalf("empty removal invented an empty journal: %v", err)
	}
	for range 2 {
		if err := store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID); err != nil {
			t.Fatal("missing journal deletion is not idempotent", err)
		}
	}
	if writer.deletes != 0 || writer.updates != 0 || writer.creates != 0 {
		t.Fatal("missing journal caused effects")
	}
	assertJournalAgentUntouched(t, writer.Client, plan)
}

func TestGrantJournalRevokeOnlyRecordedSubset(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	recordJournal(t, store, plan, plan.Identity.Bindings[:1])
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); !errors.Is(err, ErrOwnership) {
		t.Fatalf("unrecorded binding request accepted: %v", err)
	}
	if writer.deletes != 0 {
		t.Fatal("valid prefix was revoked before detecting unrecorded suffix")
	}
	recordJournal(t, store, plan, plan.Identity.Bindings)
	ref := plan.Identity.Bindings[0]
	binding := journalBindingObject(t, writer.Client, ref)
	if err := writer.Client.Delete(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, []BindingReference{ref}); err != nil {
		t.Fatal("already-absent recorded binding was not idempotent", err)
	}
	if writer.deletes != 0 {
		t.Fatal("absent binding caused a redundant delete")
	}
	assertJournalInventory(t, store, plan, plan.Identity.Bindings[1:])
	journalBindingObject(t, writer.Client, plan.Identity.Bindings[1])
	assertJournalProtection(t, store, writer.Client, plan)
}

func TestGrantJournalPartialRevocationRetainsInventoryForRetry(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	recordJournal(t, store, plan, plan.Identity.Bindings)
	failure := errors.New("synthetic RoleBinding deletion failure")
	writer.beforeDelete = func(obj client.Object, options client.DeleteOptions) error {
		if options.Preconditions == nil || options.Preconditions.UID == nil || options.Preconditions.ResourceVersion == nil ||
			*options.Preconditions.UID != obj.GetUID() || *options.Preconditions.ResourceVersion != obj.GetResourceVersion() {
			t.Fatal("binding delete lacked exact UID/RV preconditions")
		}
		if _, ok := obj.(*rbacv1.RoleBinding); ok {
			return failure
		}
		return nil
	}
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); !errors.Is(err, failure) {
		t.Fatal("partial revocation failure lost", err)
	}
	if writer.deletes != 2 || writer.updates != 0 {
		t.Fatal("partial revocation did not try all bindings or prematurely removed inventory")
	}
	assertJournalInventory(t, store, plan, plan.Identity.Bindings)
	assertJournalProtection(t, store, writer.Client, plan)
	writer.beforeDelete = nil
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); err != nil {
		t.Fatal("partial revocation was not retriable", err)
	}
	assertJournalInventory(t, store, plan, nil)
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, nil); err != nil {
		t.Fatal(err)
	}
	assertJournalProtection(t, store, writer.Client, plan)
	for _, obj := range []client.Object{
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "view"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "read-nodes"}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: plan.Identity.ServiceAccountName, Namespace: plan.Namespace}},
	} {
		if err := writer.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			t.Fatal("journal cleanup touched a shared role or ServiceAccount", err)
		}
	}
}

func TestGrantJournalTerminatingBindingRemainsPending(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	recordJournal(t, store, plan, plan.Identity.Bindings)
	binding := journalBindingObject(t, writer.Client, plan.Identity.Bindings[0])
	binding.SetFinalizers([]string{"example.com/binding-cleanup"})
	if err := writer.Client.Update(ctx, binding); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); !errors.Is(err, ErrRevocationPending) {
			t.Fatalf("terminating binding treated as absent: %v", err)
		}
		assertJournalInventory(t, store, plan, plan.Identity.Bindings)
		assertJournalProtection(t, store, writer.Client, plan)
	}
	binding = journalBindingObject(t, writer.Client, plan.Identity.Bindings[0])
	if binding.GetDeletionTimestamp().IsZero() {
		t.Fatal("binding deletion was never requested")
	}
	binding.SetFinalizers(nil)
	if err := writer.Client.Update(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); err != nil {
		t.Fatal(err)
	}
	assertJournalInventory(t, store, plan, nil)
	assertJournalProtection(t, store, writer.Client, plan)
}

func TestGrantJournalAuthoritativeBindingReadErrorsRetainInventory(t *testing.T) {
	plan, store, writer := journalFixture(t)
	recordJournal(t, store, plan, plan.Identity.Bindings)
	failure := errors.New("synthetic authoritative binding read unavailable")
	reader := store.APIReader
	store.APIReader = journalTestReader{Reader: reader, get: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.ConfigMap); !ok {
			return failure
		}
		return reader.Get(ctx, key, obj, opts...)
	}}
	if err := store.Revoke(context.Background(), plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); !errors.Is(err, failure) {
		t.Fatal("binding read error became absence", err)
	}
	if writer.reads != 0 || writer.deletes != 0 || writer.updates != 0 {
		t.Fatal("read failure caused fallback or effects")
	}
	assertJournalInventory(t, store, plan, plan.Identity.Bindings)
	assertJournalProtection(t, store, writer.Client, plan)
}

func TestGrantJournalTerminatingStateAllowsOnlyProtectedCleanup(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	recordJournal(t, store, plan, plan.Identity.Bindings)
	cm := readJournalCM(t, store, writer.Client, plan)
	if err := writer.Client.Delete(ctx, cm); err != nil {
		t.Fatal(err)
	}
	cm = readJournalCM(t, store, writer.Client, plan)
	if cm.DeletionTimestamp.IsZero() {
		t.Fatal("fixture did not simulate foreground garbage collection")
	}
	assertJournalInventory(t, store, plan, plan.Identity.Bindings)
	for _, refs := range [][]BindingReference{nil, plan.Identity.Bindings} {
		if err := store.Record(ctx, plan.AgentName, plan.Binding.AgentUID, refs); !apierrors.IsConflict(err) {
			t.Fatal("terminating journal accepted a write-ahead record", err)
		}
	}
	if writer.updates != 0 {
		t.Fatal("Record changed a terminating journal")
	}
	if err := store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID); !errors.Is(err, ErrRevocationPending) {
		t.Fatal("nonempty terminating journal could be deleted", err)
	}
	assertJournalProtection(t, store, writer.Client, plan)
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); err != nil {
		t.Fatal(err)
	}
	assertJournalInventory(t, store, plan, nil)
	assertJournalProtection(t, store, writer.Client, plan)
	if err := store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID); err != nil {
		t.Fatal("empty terminating journal could not release its own finalizer", err)
	}
	if err := writer.Client.Get(ctx, client.ObjectKeyFromObject(cm), &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Fatal("terminating journal remained after finalizer release", err)
	}
	assertJournalAgentUntouched(t, writer.Client, plan)
}

func TestGrantJournalTerminatingStateWithoutFinalizerCannotAuthorizeCleanup(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	recordJournal(t, store, plan, plan.Identity.Bindings)
	cm := readJournalCM(t, store, writer.Client, plan)
	cm.Finalizers = []string{"example.com/other-cleanup"}
	if err := writer.Client.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	if err := writer.Client.Delete(ctx, cm); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); !errors.Is(err, ErrOwnership) {
		t.Fatal("unprotected terminating journal authorized revocation", err)
	}
	if err := store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID); !errors.Is(err, ErrOwnership) {
		t.Fatal("unprotected terminating journal authorized cleanup", err)
	}
	if writer.updates != 0 || writer.deletes != 0 {
		t.Fatal("unprotected terminating record caused effects")
	}
	assertJournalAgentUntouched(t, writer.Client, plan)
}

func TestGrantJournalPreservesForeignMetadata(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	recordJournal(t, store, plan, plan.Identity.Bindings)
	cm := readJournalCM(t, store, writer.Client, plan)
	cm.Labels["example.com/label"] = "external"
	cm.Annotations["example.com/annotation"] = "synthetic external metadata"
	cm.Finalizers = append(cm.Finalizers, "example.com/other-cleanup")
	if err := writer.Client.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	recordJournal(t, store, plan, nil)
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); err != nil {
		t.Fatal(err)
	}
	observed := readJournalCM(t, store, writer.Client, plan)
	if !reflect.DeepEqual(cm.Labels, observed.Labels) || !reflect.DeepEqual(cm.Annotations, observed.Annotations) ||
		!slices.Equal(cm.Finalizers, observed.Finalizers) || !reflect.DeepEqual(cm.OwnerReferences, observed.OwnerReferences) {
		t.Fatal("Record or Revoke stripped foreign metadata")
	}
	writer.beforeDelete = func(obj client.Object, options client.DeleteOptions) error {
		if _, ok := obj.(*corev1.ConfigMap); !ok {
			t.Fatal("Delete touched something other than the journal")
		}
		if slices.Contains(obj.GetFinalizers(), GrantJournalFinalizer) ||
			!slices.Contains(obj.GetFinalizers(), "example.com/other-cleanup") {
			t.Fatal("Delete did not explicitly release only its own finalizer")
		}
		if options.Preconditions == nil || options.Preconditions.UID == nil ||
			options.Preconditions.ResourceVersion == nil || *options.Preconditions.UID != obj.GetUID() ||
			*options.Preconditions.ResourceVersion != obj.GetResourceVersion() {
			t.Fatal("journal deletion lacked UID and resourceVersion preconditions")
		}
		return nil
	}
	if err := store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID); err != nil {
		t.Fatal(err)
	}
	observed = readJournalCM(t, store, writer.Client, plan)
	if observed.DeletionTimestamp.IsZero() || !slices.Equal(observed.Finalizers, []string{"example.com/other-cleanup"}) ||
		!reflect.DeepEqual(cm.Labels, observed.Labels) || !reflect.DeepEqual(cm.Annotations, observed.Annotations) {
		t.Fatal("Delete stripped foreign metadata or failed to request deletion")
	}
	assertJournalAgentUntouched(t, writer.Client, plan)
}

func TestGrantJournalCreateRaceReturnsConflictWithoutAdoption(t *testing.T) {
	plan, store, writer := journalFixture(t)
	other := plan.Identity.Bindings[1]
	writer.beforeCreate = func(obj client.Object) error {
		competing := obj.(*corev1.ConfigMap).DeepCopy()
		competing.UID = "competing-journal"
		competing.Data[GrantJournalDataKey] = journalRaw(t, plan, []BindingReference{other})
		return writer.Client.Create(context.Background(), competing)
	}
	if err := store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings[:1]); !apierrors.IsConflict(err) {
		t.Fatalf("create race was not a conflict: %v", err)
	}
	if writer.updates != 0 || writer.creates != 1 {
		t.Fatal("create race retried or adopted concurrent state")
	}
	assertJournalInventory(t, store, plan, []BindingReference{other})
	assertJournalProtection(t, store, writer.Client, plan)
}

func TestGrantJournalCASConflictsDoNotLoseConcurrentInventory(t *testing.T) {
	for _, operation := range []string{"Record", "Revoke", "Delete"} {
		t.Run(operation, func(t *testing.T) {
			plan, store, writer := journalFixture(t)
			ctx := context.Background()
			previous := plan.Identity.Bindings[:1]
			if operation == "Delete" {
				previous = nil
			}
			recordJournal(t, store, plan, previous)
			concurrent := plan.Identity.Bindings[1]
			writer.beforeUpdate = func(obj client.Object) error {
				if obj.GetUID() == "" || obj.GetResourceVersion() == "" {
					t.Fatal("journal update lacked an observed UID/resourceVersion")
				}
				current := readJournalCM(t, store, writer.Client, plan)
				current.Data[GrantJournalDataKey] = journalRaw(t, plan, append(slices.Clone(previous), concurrent))
				current.Annotations["example.com/concurrent"] = "preserve"
				return writer.Client.Update(ctx, current)
			}
			var err error
			switch operation {
			case "Record":
				err = store.Record(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings[1:])
			case "Revoke":
				err = store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, previous)
			case "Delete":
				err = store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID)
			}
			if !apierrors.IsConflict(err) {
				t.Fatalf("stale CAS did not return conflict: %v", err)
			}
			if writer.updates != 1 {
				t.Fatal("stale CAS was retried against a newer inventory")
			}
			assertJournalInventory(t, store, plan, append(slices.Clone(previous), concurrent))
			cm := readJournalCM(t, store, writer.Client, plan)
			if cm.Annotations["example.com/concurrent"] != "preserve" {
				t.Fatal("concurrent metadata was overwritten")
			}
			assertJournalProtection(t, store, writer.Client, plan)
			if operation == "Revoke" {
				writer.beforeUpdate = nil
				if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, previous); err != nil {
					t.Fatal("stale-CAS revocation could not be retried", err)
				}
				assertJournalInventory(t, store, plan, []BindingReference{concurrent})
			}
		})
	}
}

func TestGrantJournalRequiresAuthoritativePersistenceReadback(t *testing.T) {
	for _, creating := range []bool{false, true} {
		for _, failure := range []string{"read error", "replaced UID", "changed version", "changed contents", "terminating", "missing finalizer"} {
			t.Run(fmt.Sprintf("create=%v/%s", creating, failure), func(t *testing.T) {
				plan, store, writer := journalFixture(t)
				if !creating {
					recordJournal(t, store, plan, nil)
				}
				baseReader := store.APIReader
				reads := 0
				readError := errors.New("synthetic journal readback unavailable")
				store.APIReader = journalTestReader{Reader: baseReader, get: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					reads++
					if reads > 1 && failure == "read error" {
						return readError
					}
					if err := baseReader.Get(ctx, key, obj, opts...); err != nil {
						return err
					}
					if reads > 1 {
						cm := obj.(*corev1.ConfigMap)
						switch failure {
						case "replaced UID":
							cm.UID = "replacement-journal"
						case "changed version":
							cm.ResourceVersion = "999"
						case "changed contents":
							cm.Data[GrantJournalDataKey] = journalRaw(t, plan, []BindingReference{})
						case "terminating":
							now := metav1.Now()
							cm.DeletionTimestamp = &now
						case "missing finalizer":
							cm.Finalizers = nil
						}
					}
					return nil
				}}
				if err := store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); err == nil {
					t.Fatal("Record succeeded without confirming durable, unchanged, protected inventory")
				}
				if writer.deletes != 0 {
					t.Fatal("readback error caused cleanup effects")
				}
				assertJournalProtection(t, store, writer.Client, plan)
			})
		}
	}
}

func TestGrantJournalValidatesWriteResponse(t *testing.T) {
	for _, creating := range []bool{false, true} {
		t.Run(fmt.Sprint(creating), func(t *testing.T) {
			plan, store, writer := journalFixture(t)
			if !creating {
				recordJournal(t, store, plan, nil)
			}
			mutate := func(obj client.Object) error {
				obj.(*corev1.ConfigMap).Data[GrantJournalDataKey] = journalRaw(t, plan, []BindingReference{})
				return nil
			}
			writer.afterCreate, writer.afterUpdate = mutate, mutate
			if err := store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); !apierrors.IsConflict(err) {
				t.Fatalf("mutated write response accepted: %v", err)
			}
			assertJournalProtection(t, store, writer.Client, plan)
		})
	}
}

func TestGrantJournalDeleteRequiresEmptyInventory(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	recordJournal(t, store, plan, plan.Identity.Bindings)
	if err := store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID); !errors.Is(err, ErrRevocationPending) {
		t.Fatal("nonempty journal was deleted", err)
	}
	if writer.updates != 0 || writer.deletes != 0 {
		t.Fatal("nonempty journal deletion released protection")
	}
	assertJournalProtection(t, store, writer.Client, plan)
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ctx, plan.AgentName, plan.Binding.AgentUID); !apierrors.IsNotFound(err) {
		t.Fatal("empty journal was not deleted", err)
	}
	if err := store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID); err != nil {
		t.Fatal("journal delete retry is not idempotent", err)
	}
	assertJournalAgentUntouched(t, writer.Client, plan)
}

func TestGrantJournalDeleteErrorsRetainProtectionWhenCASCanRestoreIt(t *testing.T) {
	for _, failureAt := range []string{"release", "readback", "delete"} {
		t.Run(failureAt, func(t *testing.T) {
			plan, store, writer := journalFixture(t)
			ctx := context.Background()
			recordJournal(t, store, plan, nil)
			failure := errors.New("synthetic journal cleanup failure")
			if failureAt == "release" {
				writer.beforeUpdate = func(client.Object) error { return failure }
			}
			if failureAt == "readback" {
				baseReader := store.APIReader
				count := 0
				store.APIReader = journalTestReader{Reader: baseReader, get: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					count++
					if count == 2 {
						return failure
					}
					return baseReader.Get(ctx, key, obj, opts...)
				}}
			}
			if failureAt == "delete" {
				writer.beforeDelete = func(client.Object, client.DeleteOptions) error { return failure }
			}
			if err := store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID); !errors.Is(err, failure) {
				t.Fatal("cleanup failure was lost", err)
			}
			assertJournalProtection(t, store, writer.Client, plan)
			assertJournalInventory(t, store, plan, nil)
			writer.beforeUpdate, writer.beforeDelete = nil, nil
			store.APIReader = writer.Client
			if err := store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID); err != nil {
				t.Fatal("failed cleanup was not retriable", err)
			}
			assertJournalAgentUntouched(t, writer.Client, plan)
		})
	}
}

func TestGrantJournalDeleteConflictNeverOverwritesConcurrentState(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	recordJournal(t, store, plan, nil)
	writer.beforeDelete = func(obj client.Object, _ client.DeleteOptions) error {
		cm := readJournalCM(t, store, writer.Client, plan)
		cm.Annotations["example.com/concurrent"] = "preserve"
		cm.Finalizers = append(cm.Finalizers, GrantJournalFinalizer)
		cm.Data[GrantJournalDataKey] = journalRaw(t, plan, plan.Identity.Bindings)
		return writer.Client.Update(ctx, cm)
	}
	if err := store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID); !apierrors.IsConflict(err) {
		t.Fatalf("delete/finalizer-restoration race was not a conflict: %v", err)
	}
	assertJournalProtection(t, store, writer.Client, plan)
	assertJournalInventory(t, store, plan, plan.Identity.Bindings)
	if readJournalCM(t, store, writer.Client, plan).Annotations["example.com/concurrent"] != "preserve" {
		t.Fatal("finalizer restoration overwrote concurrent state")
	}
}

func TestGrantJournalPersistenceFailuresRetainInventory(t *testing.T) {
	for _, operation := range []string{"Create", "Record", "Revoke"} {
		t.Run(operation, func(t *testing.T) {
			plan, store, writer := journalFixture(t)
			ctx := context.Background()
			failure := errors.New("synthetic journal persistence failure")
			if operation == "Create" {
				writer.beforeCreate = func(client.Object) error { return failure }
			} else {
				recordJournal(t, store, plan, plan.Identity.Bindings)
				writer.beforeUpdate = func(client.Object) error { return failure }
			}
			var err error
			if operation == "Revoke" {
				err = store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings)
			} else {
				var desired []BindingReference
				if operation == "Record" {
					ref := plan.Identity.Bindings[0]
					ref.UID = journalBindingObject(t, writer.Client, ref).GetUID()
					desired = []BindingReference{ref}
				}
				err = store.Record(ctx, plan.AgentName, plan.Binding.AgentUID, desired)
			}
			if !errors.Is(err, failure) {
				t.Fatal("persistence failure was lost", err)
			}
			if operation == "Create" {
				if _, err := store.Read(ctx, plan.AgentName, plan.Binding.AgentUID); !apierrors.IsNotFound(err) {
					t.Fatal("failed create invented durable empty inventory", err)
				}
			} else {
				assertJournalInventory(t, store, plan, plan.Identity.Bindings)
				assertJournalProtection(t, store, writer.Client, plan)
			}
			writer.beforeCreate, writer.beforeUpdate = nil, nil
			if operation == "Revoke" {
				if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); err != nil {
					t.Fatal("failed revocation persistence could not be retried", err)
				}
				assertJournalInventory(t, store, plan, nil)
			}
			assertJournalAgentUntouched(t, writer.Client, plan)
		})
	}
}

func TestGrantJournalReadFailureDoesNotCreateOrCleanUp(t *testing.T) {
	plan, store, writer := journalFixture(t)
	recordJournal(t, store, plan, plan.Identity.Bindings)
	failure := errors.New("synthetic authoritative journal read unavailable")
	reader := store.APIReader
	store.APIReader = journalTestReader{Reader: reader, get: func(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
		return failure
	}}
	if _, err := store.Read(context.Background(), plan.AgentName, plan.Binding.AgentUID); !errors.Is(err, failure) {
		t.Fatal("Read lost the authoritative error", err)
	}
	for _, operation := range []func() error{
		func() error { return store.Record(context.Background(), plan.AgentName, plan.Binding.AgentUID, nil) },
		func() error {
			return store.Revoke(context.Background(), plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings)
		},
		func() error { return store.Delete(context.Background(), plan.AgentName, plan.Binding.AgentUID) },
	} {
		if err := operation(); !errors.Is(err, failure) {
			t.Fatal("operation lost the authoritative read error", err)
		}
	}
	if writer.creates != 1 || writer.updates != 0 || writer.deletes != 0 || writer.reads != 0 {
		t.Fatal("failed authoritative read triggered effects or cached fallback")
	}
	assertJournalProtection(t, store, writer.Client, plan)
}

func TestGrantJournalMustConfirmBindingAbsenceAfterDelete(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	recordJournal(t, store, plan, plan.Identity.Bindings)
	failure := errors.New("synthetic absence confirmation failure")
	reader := store.APIReader
	counts := map[journalBindingKey]int{}
	store.APIReader = journalTestReader{Reader: reader, get: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.ConfigMap); !ok {
			id := journalBindingKey{Name: key.Name, Namespace: key.Namespace}
			counts[id]++
			if counts[id] > 1 {
				return failure
			}
		}
		return reader.Get(ctx, key, obj, opts...)
	}}
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); !errors.Is(err, failure) {
		t.Fatal("binding delete request was mistaken for confirmed absence", err)
	}
	if writer.deletes != 2 || writer.updates != 0 {
		t.Fatal("unconfirmed deletion removed journal entries")
	}
	assertJournalInventory(t, store, plan, plan.Identity.Bindings)
	assertJournalProtection(t, store, writer.Client, plan)
	store.APIReader = reader
	if err := store.Revoke(ctx, plan.AgentName, plan.Binding.AgentUID, plan.Identity.Bindings); err != nil {
		t.Fatal(err)
	}
	assertJournalInventory(t, store, plan, nil)
}

func TestGrantJournalDeleteWillNotDeleteReplacementJournal(t *testing.T) {
	plan, store, writer := journalFixture(t)
	ctx := context.Background()
	recordJournal(t, store, plan, nil)
	replaced := false
	writer.afterUpdate = func(obj client.Object) error {
		if replaced {
			return nil
		}
		cm := obj.(*corev1.ConfigMap)
		if slices.Contains(cm.Finalizers, GrantJournalFinalizer) {
			t.Fatal("fixture expected explicit finalizer release")
		}
		replacement := cm.DeepCopy()
		replacement.UID, replacement.ResourceVersion = "replacement-journal", ""
		replacement.Finalizers = []string{GrantJournalFinalizer}
		replacement.Data[GrantJournalDataKey] = journalRaw(t, plan, plan.Identity.Bindings)
		if err := writer.Client.Delete(ctx, cm); err != nil {
			return err
		}
		if err := writer.Client.Create(ctx, replacement); err != nil {
			return err
		}
		replaced = true
		return nil
	}
	if err := store.Delete(ctx, plan.AgentName, plan.Binding.AgentUID); !apierrors.IsConflict(err) {
		t.Fatalf("replacement journal was not rejected: %v", err)
	}
	if writer.deletes != 0 {
		t.Fatal("replacement journal was submitted for deletion")
	}
	assertJournalInventory(t, store, plan, plan.Identity.Bindings)
	assertJournalProtection(t, store, writer.Client, plan)
}

func TestGrantJournalJSONAllocationBounds(t *testing.T) {
	for _, raw := range []string{
		`{"bindings":[` + strings.Repeat("{},", MaxGrantJournalReferences) + `{}]}`,
		`{"bindings":[` + strings.Repeat("0,", MaxGrantJournalReferences) + `0]}`,
		`{"bindings":[` + strings.Repeat("[", 50) + strings.Repeat("]", 50) + `]}`,
	} {
		if err := validateJournalJSON(raw); err == nil {
			t.Fatal("unbounded reference allocation or nesting was permitted")
		}
	}
	for _, raw := range []string{"}", "]", "[}", `{"bindings":]}`, `{"bindings":null}`} {
		if err := validateJournalJSON(raw); err == nil {
			t.Fatalf("malformed JSON accepted: %q", raw)
		}
	}
}
