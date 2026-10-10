**controller:** the operator's memory no longer grows with every ConfigMap and
Service in the cluster. Discovery reads them straight from the API server
instead of through cluster-wide informers, and cached objects (Pods included)
are kept without their `managedFields`
