**controller:** an MCPServerGroup counts starting members, and members with no
state yet, in the new `status.initializingCount` instead of `coldCount`, and
its member list no longer carries `lastHealthCheck`, so a member's health
probe no longer rewrites the group status
