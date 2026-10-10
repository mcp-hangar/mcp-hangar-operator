**infra:** the pod-registration webhook reads the MCPServer straight from the
API server, so a pod applied right after its server is no longer denied
because the cache had not caught up; with webhooks on, a replica is ready only
once its webhook server listens; and the kustomize Deployment's
`terminationGracePeriodSeconds` is 15 s, longer than the 10 s drain, so the
leader lease release is not cut off
