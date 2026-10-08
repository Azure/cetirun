// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package sandboxcoordination serializes participating execution and source
// changes. Its records are not runtime permits or evidence of writer exclusion.
package sandboxcoordination

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxwriter"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	pathvalidation "k8s.io/apimachinery/pkg/api/validation/path"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	strictjson "sigs.k8s.io/json"
)

const (
	DataKey    = "coordination.json"
	Finalizer  = "sandbox.cetirun.microsoft.com/coordination"
	MaxBytes   = 512 * 1024
	MaxEntries = 128
	MaxSources = 128
	Reserved   = Phase("reserved")
	Starting   = Phase("starting")
	Running    = Phase("running")
	Stopping   = Phase("stopping")
	Changing   = Phase("changing")
)

var (
	ErrConflict    = errors.New("Sandbox coordination conflicts with an outstanding operation")
	ErrUnavailable = errors.New("Sandbox coordination authority is unavailable")
)

type Phase string

type EnrolledTarget struct {
	sandbox.ExecutionIdentity
	Writer        sandboxwriter.Writer
	WriterJournal w.ObjectIdentity
}

// Intent contains identities and digests only. Expected must be independently
// enrolled before consulting an enforcement observation, not copied from it.
type Intent struct {
	Agent           w.ObjectIdentity           `json:"agent"`
	AgentGeneration int64                      `json:"agentGeneration"`
	Namespace       w.ObjectIdentity           `json:"namespace"`
	Home            w.ObjectIdentity           `json:"home"`
	Workload        w.ObjectIdentity           `json:"workload"`
	WorkloadDigest  string                     `json:"workloadDigest"`
	Expected        sandbox.ExecutionIdentity  `json:"expected"`
	Writer          sandboxwriter.Writer       `json:"writer"`
	WriterJournal   w.ObjectIdentity           `json:"writerJournal"`
	Preparation     w.SourceReference          `json:"preparation"`
	Placement       *w.PlacementBinding        `json:"placement"`
	Sources         []w.SourceReference        `json:"sources"`
	IngressMode     api.AgentPolicyIngressMode `json:"ingressMode"`
}

func (intent Intent) Validate() error {
	return validateIntent(intent)
}

type Execution struct {
	ID     string `json:"id"`
	Intent Intent `json:"intent"`
	Phase  Phase  `json:"phase"`
}

// Change reserves one exact API mutation. The caller must retain it across an
// uncertain outcome until the API write is definitively observed or excluded.
type Change struct {
	ID            string            `json:"id"`
	Before        w.SourceReference `json:"before"`
	DesiredDigest string            `json:"desiredDigest"`
	Phase         Phase             `json:"phase"`
}

type Ticket struct {
	AuthorityUID types.UID
	Execution    Execution
}

func (ticket Ticket) DeepCopy() Ticket {
	ticket.Execution = cloneExecution(ticket.Execution)
	return ticket
}

type ChangeTicket struct {
	AuthorityUID types.UID
	Change       Change
}

type record struct {
	Version    int         `json:"version"`
	Executions []Execution `json:"executions"`
	Changes    []Change    `json:"changes"`
}

// Store uses one UID-pinned, protected object so acquisition is atomic across
// shared sources. There is deliberately no TTL, stealing, or create-on-missing.
// All source writers must participate before a deployment can use this store
// for execution. Read-only source rechecks alone do not establish that boundary.
type Store struct {
	Client    client.Client
	APIReader client.Reader
	Namespace string
	Identity  w.ObjectIdentity
}

// NewState is for explicit trusted installation, before any execution is
// possible. Pin the server-issued UID in Store.Identity after creating it.
func NewState(namespace, name string) (*corev1.ConfigMap, error) {
	if len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 {
		return nil, fmt.Errorf("%w: valid state namespace and name are required", ErrUnavailable)
	}
	object := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: namespace, Name: name, Finalizers: []string{Finalizer},
	}}
	if err := encode(object, record{Version: 1, Executions: []Execution{}, Changes: []Change{}}); err != nil {
		return nil, err
	}
	return object, nil
}

func (s Store) AcquireExecution(ctx context.Context, intent Intent) (Ticket, error) {
	if err := validateIntent(intent); err != nil {
		return Ticket{}, err
	}
	if intent.Namespace.Name == s.Namespace {
		return Ticket{}, fmt.Errorf("%w: execution cannot use the authority namespace", ErrConflict)
	}
	object, state, err := s.read(ctx)
	if err != nil {
		return Ticket{}, err
	}
	for _, existing := range state.Executions {
		if existing.Intent.Agent.UID == intent.Agent.UID {
			if equality.Semantic.DeepEqual(existing.Intent, intent) {
				return Ticket{object.UID, cloneExecution(existing)}, nil
			}
			return Ticket{}, ErrConflict
		}
		if err := compatibleExecutions(existing.Intent, intent); err != nil {
			return Ticket{}, err
		}
	}
	for _, change := range state.Changes {
		if uses(intent.Sources, change.Before) {
			return Ticket{}, ErrConflict
		}
	}
	execution := Execution{ID: rand.Text(), Intent: intent, Phase: Reserved}
	state.Executions = append(state.Executions, execution)
	if err := s.write(ctx, object, state); err != nil {
		return Ticket{}, err
	}
	ticket := Ticket{object.UID, cloneExecution(execution)}
	if err := s.Check(ctx, ticket); err != nil {
		return Ticket{}, err
	}
	return ticket, nil
}

func (s Store) AcquireChange(ctx context.Context, before w.SourceReference, desiredDigest string) (ChangeTicket, error) {
	if !validSource(before) || !digest(desiredDigest) {
		return ChangeTicket{}, fmt.Errorf("%w: change requires an exact source and desired-content digest", ErrUnavailable)
	}
	object, state, err := s.read(ctx)
	if err != nil {
		return ChangeTicket{}, err
	}
	for _, execution := range state.Executions {
		if uses(execution.Intent.Sources, before) {
			return ChangeTicket{}, ErrConflict
		}
	}
	for _, existing := range state.Changes {
		if sourceKey(existing.Before) == sourceKey(before) {
			if existing.Before == before && existing.DesiredDigest == desiredDigest {
				return ChangeTicket{object.UID, existing}, nil
			}
			return ChangeTicket{}, ErrConflict
		}
	}
	change := Change{ID: rand.Text(), Before: before, DesiredDigest: desiredDigest, Phase: Reserved}
	state.Changes = append(state.Changes, change)
	if err := s.write(ctx, object, state); err != nil {
		return ChangeTicket{}, err
	}
	ticket := ChangeTicket{object.UID, change}
	if err := s.CheckChange(ctx, ticket); err != nil {
		return ChangeTicket{}, err
	}
	return ticket, nil
}

func (s Store) ReadExecution(ctx context.Context, agentUID types.UID) (Ticket, error) {
	if agentUID == "" {
		return Ticket{}, fmt.Errorf("%w: Agent UID is required", ErrUnavailable)
	}
	object, state, err := s.read(ctx)
	if err != nil {
		return Ticket{}, err
	}
	for _, execution := range state.Executions {
		if execution.Intent.Agent.UID == agentUID {
			return Ticket{object.UID, cloneExecution(execution)}, nil
		}
	}
	return Ticket{}, fmt.Errorf("%w: execution is not recorded", ErrUnavailable)
}

func (s Store) Check(ctx context.Context, ticket Ticket) error {
	object, state, err := s.read(ctx)
	if err != nil {
		return err
	}
	if ticket.AuthorityUID == object.UID {
		for _, execution := range state.Executions {
			if equality.Semantic.DeepEqual(execution, ticket.Execution) {
				return ctx.Err()
			}
		}
	}
	return ErrConflict
}

func (s Store) CheckChange(ctx context.Context, ticket ChangeTicket) error {
	object, state, err := s.read(ctx)
	if err != nil {
		return err
	}
	if ticket.AuthorityUID == object.UID && slices.Contains(state.Changes, ticket.Change) {
		return ctx.Err()
	}
	return ErrConflict
}

// BeginChange records submission intent before the participating API writer
// acts. After an uncertain write, only outcome observation is allowed.
func (s Store) BeginChange(ctx context.Context, ticket ChangeTicket) (ChangeTicket, error) {
	if ticket.Change.Phase != Reserved {
		return ChangeTicket{}, ErrConflict
	}
	object, state, err := s.read(ctx)
	if err != nil {
		return ChangeTicket{}, err
	}
	if object.UID != ticket.AuthorityUID {
		return ChangeTicket{}, ErrConflict
	}
	for i, change := range state.Changes {
		if change == ticket.Change {
			state.Changes[i].Phase = Changing
			if err := s.write(ctx, object, state); err != nil {
				return ChangeTicket{}, err
			}
			ticket.Change.Phase = Changing
			if err := s.CheckChange(ctx, ticket); err != nil {
				return ChangeTicket{}, err
			}
			return ticket, nil
		}
	}
	return ChangeTicket{}, ErrConflict
}

// BeginStart is not retryable. Only the caller whose durable transition and
// read-back succeeded may submit Start once. A recovered Starting record must
// be observed or stopped, never submitted again, even if no Pod is visible.
func (s Store) BeginStart(ctx context.Context, ticket Ticket) (Ticket, error) {
	if ticket.Execution.Phase != Reserved {
		return Ticket{}, ErrConflict
	}
	return s.transition(ctx, ticket, Starting)
}

func (s Store) MarkRunning(ctx context.Context, ticket Ticket) (Ticket, error) {
	if ticket.Execution.Phase != Starting {
		return Ticket{}, ErrConflict
	}
	return s.transition(ctx, ticket, Running)
}

func (s Store) BeginStop(ctx context.Context, ticket Ticket) (Ticket, error) {
	if ticket.Execution.Phase == Stopping {
		return ticket, s.Check(ctx, ticket)
	}
	return s.transition(ctx, ticket, Stopping)
}

func (s Store) transition(ctx context.Context, ticket Ticket, next Phase) (Ticket, error) {
	object, state, err := s.read(ctx)
	if err != nil {
		return Ticket{}, err
	}
	if object.UID != ticket.AuthorityUID {
		return Ticket{}, ErrConflict
	}
	for i, execution := range state.Executions {
		if equality.Semantic.DeepEqual(execution, ticket.Execution) {
			state.Executions[i].Phase = next
			if err := s.write(ctx, object, state); err != nil {
				return Ticket{}, err
			}
			ticket.Execution.Phase = next
			if err := s.Check(ctx, ticket); err != nil {
				return Ticket{}, err
			}
			return ticket, nil
		}
	}
	return Ticket{}, ErrConflict
}

// ReleaseExecution must follow route withdrawal AND independently verified
// durable writer exclusion/storage release. Entering Stopping is not evidence.
func (s Store) ReleaseExecution(ctx context.Context, ticket Ticket) error {
	if ticket.Execution.Phase != Stopping {
		return ErrConflict
	}
	object, state, err := s.read(ctx)
	if err != nil {
		return err
	}
	if object.UID != ticket.AuthorityUID {
		return ErrConflict
	}
	for i, execution := range state.Executions {
		if equality.Semantic.DeepEqual(execution, ticket.Execution) {
			state.Executions = slices.Delete(state.Executions, i, i+1)
			return s.write(ctx, object, state)
		}
	}
	return ErrConflict
}

// ReleaseChange requires a definitive outcome, including exclusion of delayed
// submissions under this ticket. A timeout or a source GET is not such an
// outcome. The trusted mutation integration, not this store, supplies it.
func (s Store) ReleaseChange(ctx context.Context, ticket ChangeTicket) error {
	if ticket.Change.Phase != Changing {
		return ErrConflict
	}
	object, state, err := s.read(ctx)
	if err != nil {
		return err
	}
	if object.UID != ticket.AuthorityUID {
		return ErrConflict
	}
	for i, change := range state.Changes {
		if change == ticket.Change {
			state.Changes = slices.Delete(state.Changes, i, i+1)
			return s.write(ctx, object, state)
		}
	}
	return ErrConflict
}

func (s Store) read(ctx context.Context) (*corev1.ConfigMap, record, error) {
	var state record
	if err := ctx.Err(); err != nil {
		return nil, state, err
	}
	if s.Client == nil || s.APIReader == nil || len(validation.IsDNS1123Label(s.Namespace)) != 0 ||
		!identity(s.Identity) {
		return nil, state, fmt.Errorf("%w: configured writer, authoritative reader and state identity are required", ErrUnavailable)
	}
	object := &corev1.ConfigMap{}
	if err := s.APIReader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Identity.Name}, object); err != nil {
		return nil, state, fmt.Errorf("%w: read authority: %w", ErrUnavailable, err)
	}
	if object.Name != s.Identity.Name || object.Namespace != s.Namespace ||
		object.UID != s.Identity.UID || object.ResourceVersion == "" || !object.DeletionTimestamp.IsZero() ||
		len(object.OwnerReferences) != 0 || !slices.Contains(object.Finalizers, Finalizer) ||
		len(object.Data) != 1 || len(object.BinaryData) != 0 {
		return nil, state, fmt.Errorf("%w: authority identity or protection changed", ErrUnavailable)
	}
	raw := object.Data[DataKey]
	if len(raw) == 0 || len(raw) > MaxBytes {
		return nil, state, fmt.Errorf("%w: authority exceeds its content bound", ErrUnavailable)
	}
	problems, err := strictjson.UnmarshalStrict([]byte(raw), &state)
	if err != nil || len(problems) != 0 {
		return nil, state, fmt.Errorf("%w: malformed, duplicate or unknown authority fields", ErrUnavailable)
	}
	if err := validateRecord(state); err != nil {
		return nil, state, err
	}
	whole, err := json.Marshal(object)
	if err != nil || len(whole) > MaxBytes {
		return nil, state, fmt.Errorf("%w: authority exceeds its object bound", ErrUnavailable)
	}
	return object, state, ctx.Err()
}

func (s Store) write(ctx context.Context, object *corev1.ConfigMap, state record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := encode(object, state); err != nil {
		return err
	}
	// Never retry a conflict against a newer set of owners.
	return s.Client.Update(ctx, object)
}

func encode(object *corev1.ConfigMap, state record) error {
	if err := validateRecord(state); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	object.Data = map[string]string{DataKey: string(raw)}
	whole, err := json.Marshal(object)
	if err != nil || len(whole) > MaxBytes {
		return fmt.Errorf("%w: authority exceeds its object bound", ErrUnavailable)
	}
	return nil
}

func validateRecord(state record) error {
	if state.Version != 1 || state.Executions == nil || state.Changes == nil ||
		len(state.Executions)+len(state.Changes) > MaxEntries {
		return fmt.Errorf("%w: invalid version, inventory or entry bound", ErrUnavailable)
	}
	ids := map[string]bool{}
	for i, execution := range state.Executions {
		if !literal(execution.ID, 128) || ids[execution.ID] ||
			!slices.Contains([]Phase{Reserved, Starting, Running, Stopping}, execution.Phase) {
			return fmt.Errorf("%w: invalid execution identity or phase", ErrUnavailable)
		}
		ids[execution.ID] = true
		if err := validateIntent(execution.Intent); err != nil {
			return err
		}
		for _, other := range state.Executions[:i] {
			if other.Intent.Agent.UID == execution.Intent.Agent.UID {
				return fmt.Errorf("%w: duplicate Agent execution", ErrUnavailable)
			}
			if err := compatibleExecutions(other.Intent, execution.Intent); err != nil {
				return err
			}
		}
	}
	keys := map[string]bool{}
	for _, change := range state.Changes {
		key := sourceKey(change.Before)
		if !literal(change.ID, 128) || ids[change.ID] || !validSource(change.Before) ||
			!digest(change.DesiredDigest) || keys[key] ||
			(change.Phase != Reserved && change.Phase != Changing) {
			return fmt.Errorf("%w: invalid or duplicate source change", ErrUnavailable)
		}
		ids[change.ID], keys[key] = true, true
		for _, execution := range state.Executions {
			if uses(execution.Intent.Sources, change.Before) {
				return fmt.Errorf("%w: source change overlaps an execution", ErrUnavailable)
			}
		}
	}
	return nil
}

func validateIntent(intent Intent) error {
	if !identity(intent.Agent) || !identity(intent.Namespace) || !identity(intent.Home) ||
		!identity(intent.Workload) || !digest(intent.WorkloadDigest) || len(validation.IsDNS1123Label(intent.Namespace.Name)) != 0 ||
		intent.AgentGeneration < 1 || intent.Expected.Binding.AgentUID != intent.Agent.UID ||
		intent.Expected.Binding.TeamUID == "" || intent.Expected.Binding.SandboxUID == "" ||
		intent.Expected.Binding.AgentPolicyUID == "" ||
		(intent.Expected.Binding.PolicyClass != "normal" && intent.Expected.Binding.PolicyClass != "admin") ||
		!literal(intent.Expected.Revision, 256) || !literal(string(intent.Expected.PodUID), 128) ||
		!literal(string(intent.Expected.NodeUID), 128) || !literal(intent.Expected.EnforcementEpoch, 128) ||
		(intent.IngressMode != api.AgentPolicyIngressAuthenticated && intent.IngressMode != api.AgentPolicyIngressDisabled) {
		return fmt.Errorf("%w: complete independently enrolled execution identity is required", ErrUnavailable)
	}
	expected := w.Expectation{Agent: intent.Agent, Namespace: intent.Namespace, Home: intent.Home, Workload: intent.Workload}
	if err := intent.Placement.MatchesExpectation(expected); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !slices.ContainsFunc(intent.Placement.Nodes, func(node w.PlacementNode) bool { return node.UID == intent.Expected.NodeUID }) {
		return fmt.Errorf("%w: expected Node is outside the lifetime placement", ErrUnavailable)
	}
	if !identity(intent.WriterJournal) || intent.WriterJournal.Name != sandboxwriter.JournalName(sandboxwriter.Lifetime{
		Agent: intent.Agent, Namespace: intent.Namespace, Home: intent.Home,
	}) || intent.Writer.Workload != intent.Workload ||
		intent.Writer.Pod.Name != intent.Workload.Name+"-0" || intent.Writer.Pod.UID != intent.Expected.PodUID ||
		intent.Writer.Node.UID != intent.Expected.NodeUID || !slices.Contains(intent.Placement.Nodes, intent.Writer.Node) ||
		intent.Writer.Epoch != intent.Expected.EnforcementEpoch || !literal(intent.Writer.MachineID, 256) ||
		!identity(intent.Writer.Storage.Volume) || !literal(intent.Writer.Storage.Driver, 253) ||
		!literal(intent.Writer.Storage.Handle, 2048) {
		return fmt.Errorf("%w: exact physical writer and enrolled journal identities are required", ErrUnavailable)
	}
	if intent.Preparation.APIVersion != "v1" || intent.Preparation.Kind != "ConfigMap" ||
		intent.Preparation.Namespace == "" || intent.Preparation.Namespace == intent.Namespace.Name ||
		!validSource(intent.Preparation) || len(intent.Sources) == 0 || len(intent.Sources) > MaxSources {
		return fmt.Errorf("%w: exact preparation state and bounded sources are required", ErrUnavailable)
	}
	keys := map[string]bool{}
	required := map[struct {
		version, kind, namespace string
		uid                      types.UID
	}]bool{
		{api.GroupVersion.String(), "Team", "", intent.Expected.Binding.TeamUID}:               false,
		{api.GroupVersion.String(), "Sandbox", "", intent.Expected.Binding.SandboxUID}:         false,
		{api.GroupVersion.String(), "AgentPolicy", "", intent.Expected.Binding.AgentPolicyUID}: false,
		{"v1", "Namespace", "", intent.Namespace.UID}:                                          false,
		{"v1", "PersistentVolumeClaim", intent.Namespace.Name, intent.Home.UID}:                false,
		{"apps/v1", "StatefulSet", intent.Namespace.Name, intent.Workload.UID}:                 false,
		{"v1", "ConfigMap", intent.Preparation.Namespace, intent.Placement.Registry.UID}:       false,
	}
	exactNames := map[struct {
		version, kind, namespace string
		uid                      types.UID
	}]string{
		{"v1", "Namespace", "", intent.Namespace.UID}:                                    intent.Namespace.Name,
		{"v1", "PersistentVolumeClaim", intent.Namespace.Name, intent.Home.UID}:          intent.Home.Name,
		{"apps/v1", "StatefulSet", intent.Namespace.Name, intent.Workload.UID}:           intent.Workload.Name,
		{"v1", "ConfigMap", intent.Preparation.Namespace, intent.Placement.Registry.UID}: intent.Placement.Registry.Name,
	}
	agentPinned, preparationPinned := false, false
	for _, source := range intent.Sources {
		key := sourceKey(source)
		if !validSource(source) || keys[key] {
			return fmt.Errorf("%w: source identities must be complete and unique", ErrUnavailable)
		}
		keys[key] = true
		identity := struct {
			version, kind, namespace string
			uid                      types.UID
		}{source.APIVersion, source.Kind, source.Namespace, source.UID}
		if name, found := exactNames[identity]; found && source.Name != name {
			return fmt.Errorf("%w: source name differs from its enrolled identity", ErrUnavailable)
		}
		if _, found := required[identity]; found {
			required[identity] = true
		}
		preparationPinned = preparationPinned || source == intent.Preparation
		if source.IsAgent() {
			if source.Name != intent.Agent.Name || source.UID != intent.Agent.UID || source.Generation != intent.AgentGeneration {
				return fmt.Errorf("%w: Agent source differs from the execution", ErrUnavailable)
			}
			agentPinned = true
		}
	}
	if !agentPinned || !preparationPinned {
		return fmt.Errorf("%w: execution must pin Agent intent and preparation state", ErrUnavailable)
	}
	for _, present := range required {
		if !present {
			return fmt.Errorf("%w: required binding, placement or storage source is missing", ErrUnavailable)
		}
	}
	return nil
}

func compatibleExecutions(a, b Intent) error {
	if a.Agent.Name == b.Agent.Name || a.Namespace.Name == b.Namespace.Name ||
		a.Namespace.UID == b.Namespace.UID || a.Home.UID == b.Home.UID ||
		a.Workload.UID == b.Workload.UID || a.Expected.PodUID == b.Expected.PodUID ||
		a.WriterJournal.UID == b.WriterJournal.UID || a.Writer.MachineID == b.Writer.MachineID ||
		a.Writer.Storage.Volume.UID == b.Writer.Storage.Volume.UID ||
		(a.Writer.Storage.Driver == b.Writer.Storage.Driver && a.Writer.Storage.Handle == b.Writer.Storage.Handle) {
		return ErrConflict
	}
	for _, left := range a.Placement.Nodes {
		for _, right := range b.Placement.Nodes {
			if left.Name == right.Name || left.UID == right.UID || left.ProviderID == right.ProviderID {
				return ErrConflict
			}
		}
	}
	pinned := make(map[string]w.SourceReference, len(a.Sources))
	for _, source := range a.Sources {
		pinned[sourceKey(source)] = source
	}
	for _, source := range b.Sources {
		if old, found := pinned[sourceKey(source)]; found && old != source {
			return ErrConflict
		}
	}
	return nil
}

func cloneExecution(execution Execution) Execution {
	execution.Intent.Sources = slices.Clone(execution.Intent.Sources)
	execution.Intent.Placement = execution.Intent.Placement.DeepCopy()
	return execution
}

func sourceKey(source w.SourceReference) string {
	return strings.Join([]string{source.APIVersion, source.Kind, source.Namespace, source.Name}, "\x00")
}

func validSource(source w.SourceReference) bool {
	version, err := schema.ParseGroupVersion(source.APIVersion)
	return err == nil && version.Version != "" && literal(source.APIVersion, 253) &&
		literal(source.Kind, 128) && literal(source.Name, 253) &&
		len(pathvalidation.IsValidPathSegmentName(source.Name)) == 0 &&
		(source.Namespace == "" || len(validation.IsDNS1123Label(source.Namespace)) == 0) &&
		literal(string(source.UID), 128) && source.ValidIdentity() &&
		(source.IsAgent() || literal(source.ResourceVersion, 256))
}

func uses(sources []w.SourceReference, target w.SourceReference) bool {
	return slices.ContainsFunc(sources, func(source w.SourceReference) bool { return sourceKey(source) == sourceKey(target) })
}

func identity(value w.ObjectIdentity) bool {
	return len(validation.IsDNS1123Subdomain(value.Name)) == 0 && literal(string(value.UID), 128)
}

func literal(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit {
		return false
	}
	for _, c := range value {
		if c < '!' || c > '~' {
			return false
		}
	}
	return true
}

func digest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == value
}
