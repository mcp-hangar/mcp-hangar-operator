package controller

import (
	"k8s.io/apimachinery/pkg/util/managedfields"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newFakeClientBuilder is fake.NewClientBuilder with a type converter its
// Server-Side Apply can use. The builder's default pairs client-go's converter
// with the deduced one, and an apply through it fails with "expected objects
// with types from the same schema"; the deduced converter alone merges as the
// API server does for the maps and atomic lists these tests assert on (#220).
func newFakeClientBuilder() *fake.ClientBuilder {
	return fake.NewClientBuilder().WithTypeConverters(managedfields.NewDeducedTypeConverter())
}
