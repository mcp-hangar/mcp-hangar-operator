**api:** `MCPDiscoverySource.spec.refreshInterval` must be a non-negative
duration. The schema accepted any string, and a value such as `banana` was
stored and then broke the operator's decoding of that source
