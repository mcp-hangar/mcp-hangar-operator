**controller:** an MCPServerGroup no longer takes a finalizer, so deleting one
no longer waits for a running operator; its metrics are cleared when the
operator sees it gone, and the finalizer is removed from existing groups
