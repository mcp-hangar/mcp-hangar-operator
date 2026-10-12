package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// fieldManager is the field manager on every Server-Side Apply this operator
// makes (#220). The NetworkPolicies and CiliumNetworkPolicies it owns used to
// be written with a full Update that replaced their labels, so a label another
// actor put on a per-server policy was gone after the next reconcile. With
// Apply the API server keeps every field this manager did not set.
const fieldManager = "mcp-hangar-operator"

// applyOwned writes obj with Server-Side Apply as fieldManager, creating it if
// absent. Ownership is forced: the operator writes these objects only after
// checking it controls them, and a field it sets must end up as it set it,
// including on the first apply after an upgrade, when the fields still belong
// to the manager of the old Update.
func applyOwned(ctx context.Context, c client.Client, obj client.Object) error {
	u, err := applyUnstructured(c.Scheme(), obj)
	if err != nil {
		return err
	}
	return c.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner(fieldManager), client.ForceOwnership)
}

// applyUnstructured is obj as an apply request: its kind set, and stripped of
// what the API server assigns rather than the caller declares.
func applyUnstructured(scheme *runtime.Scheme, obj client.Object) (*unstructured.Unstructured, error) {
	var u *unstructured.Unstructured
	if in, ok := obj.(*unstructured.Unstructured); ok {
		u = in.DeepCopy()
	} else {
		gvk, err := apiutil.GVKForObject(obj, scheme)
		if err != nil {
			return nil, fmt.Errorf("kind of %T: %w", obj, err)
		}
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			return nil, fmt.Errorf("convert %T for apply: %w", obj, err)
		}
		u = &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(gvk)
	}
	for _, f := range []string{"creationTimestamp", "resourceVersion", "uid", "generation", "managedFields"} {
		unstructured.RemoveNestedField(u.Object, "metadata", f)
	}
	unstructured.RemoveNestedField(u.Object, "status")
	return u, nil
}
