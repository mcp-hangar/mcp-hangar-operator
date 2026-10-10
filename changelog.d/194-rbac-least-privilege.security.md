**config:** the operator's ClusterRole no longer grants cluster-wide read on
`secrets` and `serviceaccounts`, which nothing in the operator reads, and the
leader-election Role no longer grants full CRUD on `configmaps`: the lock is a
Lease
