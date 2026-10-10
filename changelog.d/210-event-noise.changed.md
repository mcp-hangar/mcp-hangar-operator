**controller:** Events fire when something changes, not on every poll. An
unhealthy or unreachable remote MCPServer warned every 10 s, a healthy one
logged "is ready"/"is healthy" every 5 minutes, and every discovery refresh
emitted `SyncStarted` and `SyncCompleted`. These now fire on the condition
transition (and `SyncCompleted` when the sync outcome changes); `SyncStarted`
is gone
