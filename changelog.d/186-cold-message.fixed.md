**controller:** a cold MCPServer's `Ready` condition no longer says it "will
start on demand"; nothing scales `spec.replicas` up from 0, and the message now
says to set it to 1. The `startupTimeout` field doc says the operator does not
act on it
