package controller

import (
	"context"
	"reflect"

	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Status writes as a diff against what was read, skipped when nothing changed
// (#210).
//
// The MCPServer, discovery and egress-policy controllers used Status().Update,
// which sends the whole status and writes on every pass even when the pass
// computed the same status it read. The group controller already wrote a
// merge patch against its read and skipped the no-op; this is that pattern for
// the controllers whose status is written from many places in one reconcile.
//
// Reconcile records the object as read with withStatusBase. writeStatus then
// compares the status against that base, sends only the difference with an
// optimistic lock (a stale base is refused with 409 rather than merged), and
// moves the base forward after each write, so a reconcile that writes twice
// diffs the second write against the first.

type statusBaseKey struct{}

type statusBase struct {
	obj client.Object
}

// withStatusBase returns ctx carrying a copy of obj as the base for
// writeStatus.
func withStatusBase(ctx context.Context, obj client.Object) context.Context {
	return context.WithValue(ctx, statusBaseKey{}, &statusBase{obj: obj.DeepCopyObject().(client.Object)})
}

// statusOf returns obj's Status field; every kind passed here has one.
func statusOf(obj client.Object) reflect.Value {
	return reflect.ValueOf(obj).Elem().FieldByName("Status")
}

// writeStatus persists obj's status. Without a base for this object in ctx (a
// caller outside Reconcile, such as a test driving one step) it falls back to a
// full update.
func writeStatus(ctx context.Context, c client.Client, obj client.Object) error {
	base, _ := ctx.Value(statusBaseKey{}).(*statusBase)
	if base == nil || reflect.TypeOf(base.obj) != reflect.TypeOf(obj) ||
		client.ObjectKeyFromObject(base.obj) != client.ObjectKeyFromObject(obj) {
		return c.Status().Update(ctx, obj)
	}
	if equality.Semantic.DeepEqual(statusOf(base.obj).Interface(), statusOf(obj).Interface()) {
		return nil
	}
	// The patch base is obj with the old status, so the diff is the status
	// change alone; metadata and spec an earlier Update moved are not in it.
	orig := obj.DeepCopyObject().(client.Object)
	statusOf(orig).Set(statusOf(base.obj))
	if err := c.Status().Patch(ctx, obj, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	base.obj = obj.DeepCopyObject().(client.Object)
	return nil
}
