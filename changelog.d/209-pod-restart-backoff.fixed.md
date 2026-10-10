**controller:** a failing provider pod is restarted with a real backoff (10 s
doubling to 5 min, then every 10 min once it has failed five times in a row)
instead of at once and then never; the failure count is capped and reset by a
spec change; and a pod that exits 0 is restarted with the server
`Initializing`, not reported `Cold`
