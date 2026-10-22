**controller:** a key-pinned container-mode MCPServer that core refuses to
register, or that core does not carry a key list for, now keeps that reason in
status after its pod runs. The tools read that followed used to replace it with
`ToolsFetchFailed: provider not found`, or report Ready when core held a server
of that name registered by someone else
