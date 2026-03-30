// Package v1alpha2 contains the Hub (storage) version for CRD conversion.
//
// v1alpha2 is designated as the Hub because it is the storage version and represents
// the canonical schema. All other API versions (Spokes) convert to and from v1alpha2.
//
// The Hub() method is a marker -- it has no logic. controller-runtime uses its
// presence (via the conversion.Hub interface) to identify which version is
// authoritative.
package v1alpha2

// Hub marks MCPProvider as the Hub type for conversion.
func (*MCPProvider) Hub() {}

// Hub marks MCPProviderGroup as the Hub type for conversion.
func (*MCPProviderGroup) Hub() {}

// Hub marks MCPDiscoverySource as the Hub type for conversion.
func (*MCPDiscoverySource) Hub() {}
