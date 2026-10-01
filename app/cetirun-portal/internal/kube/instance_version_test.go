// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	"errors"
	"fmt"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestInstanceUpdateDoesNotReplayAgainstChangedOrReplacedAgent(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement=%v", replacement), func(t *testing.T) {
			instance := sampleInstance()
			instance.UID, instance.ResourceVersion = "agent-uid", "1"
			updates := 0
			fc := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(instance).
				WithInterceptorFuncs(interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					updates++
					current := &api.Agent{}
					if err := c.Get(ctx, client.ObjectKeyFromObject(instance), current); err != nil {
						return err
					}
					current.Spec.User.DisplayName = "Concurrent value"
					if replacement {
						if err := c.Delete(ctx, current); err != nil {
							return err
						}
						current.UID, current.ResourceVersion = "replacement-uid", ""
						if err := c.Create(ctx, current); err != nil {
							return err
						}
					} else if err := c.Update(ctx, current); err != nil {
						return err
					}
					return apierrors.NewConflict(api.GroupVersion.WithResource("agents").GroupResource(), obj.GetName(), fmt.Errorf("synthetic concurrent update"))
				}}).Build()
			name := "Stale value"
			_, err := NewClient(fc).UpdateInstanceByName(context.Background(), instance.Name, UpdateInstanceOpts{
				ExpectedUID: instance.UID, ExpectedResourceVersion: "1", DisplayName: &name,
			})
			if !errors.Is(err, ErrInstanceChanged) || updates != 1 {
				t.Fatalf("update was replayed or conflict hidden: updates=%d err=%v", updates, err)
			}
			current := &api.Agent{}
			if err := fc.Get(context.Background(), client.ObjectKeyFromObject(instance), current); err != nil || current.Spec.User.DisplayName != "Concurrent value" {
				t.Fatalf("concurrent value was overwritten: %v", err)
			}
		})
	}
}

func TestDeleteChecksTheDisplayedAgentVersion(t *testing.T) {
	instance := sampleInstance()
	instance.UID, instance.ResourceVersion = "new-uid", "2"
	c := NewClient(newFakeClient(instance))
	err := c.DeleteInstanceByName(context.Background(), instance.Name, InstanceVersion{UID: "old-uid", ResourceVersion: "1"})
	if !errors.Is(err, ErrInstanceChanged) {
		t.Fatalf("stale delete was accepted: %v", err)
	}
	if _, err := c.GetInstance(context.Background(), instance.Name); err != nil {
		t.Fatal("replacement Agent was deleted")
	}
}

func TestUpdateRejectsTerminatingAgent(t *testing.T) {
	instance := sampleInstance()
	now := metav1.Now()
	instance.DeletionTimestamp = &now
	instance.Finalizers = []string{api.Finalizer}
	name := "Changed"
	_, err := NewClient(newFakeClient(instance)).UpdateInstanceByName(context.Background(), instance.Name, UpdateInstanceOpts{DisplayName: &name})
	if !errors.Is(err, ErrInstanceDeleting) {
		t.Fatalf("terminating Agent update was accepted: %v", err)
	}
}
