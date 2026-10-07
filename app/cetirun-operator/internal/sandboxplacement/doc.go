// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package sandboxplacement implements an opt-in, read-only placement check.
// Its registry is a complete inventory of known Node names, UIDs and provider
// IDs, with explicit Agent reservations and individually registered platform
// DaemonSets. Unknown Nodes cannot receive direct placements or bindings.
// Reservations are bidirectional: Pods in a registered Agent namespace or
// claiming its controller ownership cannot escape to unreserved capacity or a
// different assignment. Clearing owner metadata does not erase that boundary;
// current namespace UIDs and canonical ownership are still required to enter
// the matching reserved capacity.
//
// Reserved Node deletion is denied while its reservation is present. Main
// resource updates must preserve the live registered UID and provider ID and
// cannot adopt a terminating Node. Labels are not reservation authority.
// Non-reserved Node lifecycle operations do not authorize Pod placement.
//
// The legacy core/v1 bindings CREATE endpoint is forbidden. Bindings through
// pods/binding must not contain annotations, including unchanged values.
// An already terminating Pod may remove an ordered subset of its finalizers
// only after its old/current identities and other content match, apart from
// server-managed field ownership. This non-placement cleanup still rechecks
// the subject and registry; it adds no caller exemption or new webhook-outage
// bypass.
//
// Platform Pod enrollment is deliberately manual in this pilot. Every platform
// Pod binding or update targeting a reserved Node requires an existing Pod name
// and server-issued UID in the registered DaemonSet's bootstrapPods inventory.
// A trusted controller may create an exact-template replacement, including an
// unscheduled replacement, but it cannot bind that replacement to a reserved
// Node until a trusted writer enrolls its UID. A matching ownerReference is not
// creation provenance, and read-only admission neither fabricates nor persists
// such provenance.
//
// Platform Pod comparison accounts for Kubernetes 1.37 Pod-only service-link
// defaulting and the DaemonSet controller's generated affinity, tolerations and
// template-generation label. Scheduled platform Pods may carry the two
// PodTopologyLabels additions, topology.kubernetes.io/region and
// topology.kubernetes.io/zone, only at the freshly read pinned Node's values.
// Explicit template labels are not overridden; other added labels and
// annotations remain forbidden. Binding validation checks the persisted Pod
// and the prospective Pod, including labels the Binding would apply.
//
// Deployment must independently protect the configured operator-namespace
// ConfigMap against tenant writes and enforce the cluster-wide webhook. These
// are prerequisites, not registry assertions. Platform templates must account
// for their final Pod fields; unknown admission injections are not normalized
// away. This package does not issue execution permits, change execution holds,
// write API objects, or replace protected-Pod and configuration admission.
// Bootstrap is restricted to an explicitly empty dedicated pool, with
// enrollment and in-flight-operation quiescence established separately.
// Rechecked read snapshots detect concurrent drift; they are not an atomic
// transaction, writer fence, platform qualification or automatic recovery.
package sandboxplacement
