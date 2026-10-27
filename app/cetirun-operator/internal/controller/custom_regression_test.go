// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type failStatusOnceClient struct {
	client.Client
	fail bool
}

type failStatusWriter struct {
	client.SubResourceWriter
	parent *failStatusOnceClient
}

func (c *failStatusOnceClient) Status() client.SubResourceWriter {
	return failStatusWriter{c.Client.Status(), c}
}

func (w failStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if w.parent.fail {
		w.parent.fail = false
		return fmt.Errorf("synthetic status failure")
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}
