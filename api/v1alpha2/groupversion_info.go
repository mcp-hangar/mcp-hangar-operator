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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "mcp-hangar.io", Version: "v1alpha2"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme
	AddToScheme = SchemeBuilder.AddToScheme
)
