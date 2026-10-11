package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// A reconcile that computes the status it read writes nothing (#210). With
// Status().Update every pass rewrote the status and bumped resourceVersion.
func TestWriteStatus_SteadyReconcileDoesNotWrite(t *testing.T) {
	p := testPolicy("steady", "default")
	r := newEgressReconciler(testServer("srv", "default"), p)
	r.EnforcementProbe = &networkpolicy.EnforcementProbe{Override: networkpolicy.EnforcementObserved}

	first := reconcilePolicy(t, r, p)
	second := reconcilePolicy(t, r, p)
	assert.Equal(t, first.ResourceVersion, second.ResourceVersion,
		"a reconcile that changed nothing still wrote the status")
}

// The write carries an optimistic lock: a base older than the stored object is
// refused, not merged into a status assembled from two reconciles.
func TestWriteStatus_StaleBaseIsRefused(t *testing.T) {
	p := testPolicy("stale", "default")
	r := newEgressReconciler(p)
	ctx := context.Background()

	read := &mcpv1alpha2.MCPEgressPolicy{}
	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(p), read))
	ctx = withStatusBase(ctx, read)

	// Someone else writes the status after our read.
	other := read.DeepCopy()
	other.Status.ObservedGeneration = 7
	require.NoError(t, r.Status().Update(context.Background(), other))

	read.Status.ObservedGeneration = 3
	err := writeStatus(ctx, r.Client, read)
	assert.True(t, apierrors.IsConflict(err), "a stale base must be refused with a conflict, got %v", err)
}

// A second write in the same reconcile diffs against the first, and a write
// for an object that is not the base falls back to a full update.
func TestWriteStatus_BaseMovesForwardAndOtherObjectsUpdate(t *testing.T) {
	p := testPolicy("twice", "default")
	q := testPolicy("other", "default")
	r := newEgressReconciler(p, q)
	ctx := context.Background()

	obj := &mcpv1alpha2.MCPEgressPolicy{}
	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(p), obj))
	ctx = withStatusBase(ctx, obj)

	obj.Status.ObservedGeneration = 1
	require.NoError(t, writeStatus(ctx, r.Client, obj))
	obj.Status.ObservedGeneration = 2
	require.NoError(t, writeStatus(ctx, r.Client, obj))
	got := &mcpv1alpha2.MCPEgressPolicy{}
	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(p), got))
	assert.Equal(t, int64(2), got.Status.ObservedGeneration)

	o := &mcpv1alpha2.MCPEgressPolicy{}
	require.NoError(t, r.Get(ctx, types.NamespacedName{Name: "other", Namespace: "default"}, o))
	o.Status.ObservedGeneration = 5
	require.NoError(t, writeStatus(ctx, r.Client, o))
	require.NoError(t, r.Get(ctx, types.NamespacedName{Name: "other", Namespace: "default"}, o))
	assert.Equal(t, int64(5), o.Status.ObservedGeneration)
}
