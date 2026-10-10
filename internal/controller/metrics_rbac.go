package controller

// The metrics endpoint authenticates a scraper's bearer token with a
// TokenReview and authorizes it with a SubjectAccessReview (#193). The markers
// live here because controller-gen does not read RBAC markers from package
// main, where the endpoint is configured.
//
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
