// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package configurationcoordination exposes the supported client integration
// for changing configuration resources protected by Sandbox coordination.
package configurationcoordination

import (
	"context"
	"errors"
	"fmt"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IsConflict reports a competing or mismatched coordination operation.
func IsConflict(err error) bool {
	return errors.Is(err, sandboxcoordination.ErrConflict)
}

// IsUnavailable reports an unavailable authority or an uncertain mutation
// outcome whose reservation must be retained for recovery.
func IsUnavailable(err error) bool {
	return errors.Is(err, sandboxcoordination.ErrUnavailable)
}

// Authority pins the coordination ConfigMap used by admission and all
// participating configuration writers.
type Authority struct {
	Namespace string
	Name      string
	UID       types.UID
}

// Client submits configuration updates and deletions through the existing
// durable reservation protocol. It does not create or discover an authority.
type Client struct {
	writer client.Client
	reader client.Reader
	store  sandboxcoordination.Store
}

// New constructs a coordinated configuration writer. reader must be an
// authoritative, uncached API reader; a controller-runtime direct client can
// be supplied as both writer and reader.
func New(writer client.Client, reader client.Reader, authority Authority) (*Client, error) {
	if writer == nil || reader == nil || len(validation.IsDNS1123Label(authority.Namespace)) != 0 ||
		len(validation.IsDNS1123Subdomain(authority.Name)) != 0 || !boundedIdentity(string(authority.UID), 128) {
		return nil, fmt.Errorf("configuration coordination requires a writer, authoritative reader, namespace, name, and exact authority UID")
	}
	return &Client{
		writer: writer,
		reader: reader,
		store: sandboxcoordination.Store{
			Client: writer, APIReader: reader, Namespace: authority.Namespace,
			Identity: w.ObjectIdentity{Name: authority.Name, UID: authority.UID},
		},
	}, nil
}

// Update reserves and submits one exact resource-version-conditional Update,
// observes its result, and releases the reservation only after a definitive
// applied outcome. An uncertain outcome deliberately retains the reservation.
func (c *Client) Update(ctx context.Context, before, after client.Object) error {
	adapter, err := sandboxcoordination.NewKubernetesChangeAdapter(c.writer, c.reader, before, after)
	if err != nil {
		return err
	}
	ticket, err := c.store.AcquireConfigurationChange(ctx, before, after)
	if err != nil {
		return err
	}
	return c.reconcileChange(ctx, ticket, adapter)
}

// Delete reserves and submits one exact UID/resourceVersion-conditional
// background Delete. An uncertain outcome deliberately retains the reservation.
func (c *Client) Delete(ctx context.Context, before client.Object) error {
	adapter, err := sandboxcoordination.NewKubernetesDeleteAdapter(c.writer, c.reader, before)
	if err != nil {
		return err
	}
	ticket, err := c.store.AcquireConfigurationDeletion(ctx, before)
	if err != nil {
		return err
	}
	return c.reconcileChange(ctx, ticket, adapter)
}

func (coordinator *Client) reconcileChange(ctx context.Context, ticket sandboxcoordination.ChangeTicket, adapter sandboxcoordination.ChangeAdapter) error {
	ticket, err := coordinator.store.ReconcileChange(ctx, ticket, adapter)
	if err != nil && ticket.Change.Phase == sandboxcoordination.Changing {
		return fmt.Errorf("%w: recorded configuration operation requires recovery: %w", sandboxcoordination.ErrUnavailable, err)
	}
	return err
}

func boundedIdentity(value string, limit int) bool {
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
