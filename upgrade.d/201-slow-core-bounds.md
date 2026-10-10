### Core calls give up after 5 s, and four reconciles run at once

A call from the operator to core (`--hangar-url`) now has 5 s in all, retries
included; it used to allow 10 s per attempt and up to four attempts. A core
that takes longer than 5 s to answer a health, tools or L7-policy request now
fails that call: the MCPServer or MCPEgressPolicy reports it the way it reports
an unreachable core and retries on its next requeue.

The MCPServer and MCPEgressPolicy controllers now run 4 reconciles at once
instead of 1, set with `--max-concurrent-reconciles` (at least 1). Pass
`--max-concurrent-reconciles=1` to keep the old behaviour.
