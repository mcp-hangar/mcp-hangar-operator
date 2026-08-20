// Package v1alpha2 contains API Schema definitions for the mcp-hangar.io v1alpha2 API group.
//
// v1alpha2 is the Hub version for CRD conversion. Key improvements over v1alpha1:
//   - Duration fields use metav1.Duration instead of plain strings
//   - Status conditions use standard metav1.Condition instead of a custom type
//
// +kubebuilder:object:generate=true
// +groupName=mcp-hangar.io
package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "mcp-hangar.io", Version: "v1alpha2"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	// This is the apimachinery builder rather than controller-runtime's
	// scheme.Builder helper, which is deprecated so that API packages need no
	// controller-runtime dependency (#58).
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme
	AddToScheme = SchemeBuilder.AddToScheme

	// objectTypes is appended to by each types file's init().
	objectTypes []runtime.Object
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, objectTypes...)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
