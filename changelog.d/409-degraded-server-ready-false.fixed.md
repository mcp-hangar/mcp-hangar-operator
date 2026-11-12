**controller:** an MCPServer that leaves the Ready state now reports
`Ready=False` under the reason of what went wrong. A remote server that core
reports failing, cannot be asked about, or refuses the operator's credentials
for, and a container server whose pod is pending, starting, gone or whose tool
list cannot be read, used to keep an earlier `Ready=True`, so the `Ready`
column and `kubectl wait --for=condition=Ready` reported it as serving
