# Changelog

## [0.15.3](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.15.2...v0.15.3) (2026-08-17)


### Fixed

* **api:** remove MCPServer idleTTL, healthCheck, and circuitBreaker ([#127](https://github.com/mcp-hangar/mcp-hangar-operator/issues/127)) ([317983e](https://github.com/mcp-hangar/mcp-hangar-operator/commit/317983e477e11e509af85f7e654b211293ef40f6)), closes [#120](https://github.com/mcp-hangar/mcp-hangar-operator/issues/120)
* **api:** remove MCPServer observability and unused capability declarations ([#130](https://github.com/mcp-hangar/mcp-hangar-operator/issues/130)) ([3b22f06](https://github.com/mcp-hangar/mcp-hangar-operator/commit/3b22f065191d5eaa90de63e26b7277b3e07fff00)), closes [#121](https://github.com/mcp-hangar/mcp-hangar-operator/issues/121)
* **api:** remove MCPServer spec.tools, which nothing enforced ([#126](https://github.com/mcp-hangar/mcp-hangar-operator/issues/126)) ([1600f8b](https://github.com/mcp-hangar/mcp-hangar-operator/commit/1600f8b4fe312279ac06b2677355f928ac4a35df)), closes [#119](https://github.com/mcp-hangar/mcp-hangar-operator/issues/119)
* **api:** remove MCPServerGroup failover, sessionAffinity, and circuitBreaker ([#129](https://github.com/mcp-hangar/mcp-hangar-operator/issues/129)) ([854a09a](https://github.com/mcp-hangar/mcp-hangar-operator/commit/854a09a93baf6ae06aef62d31f28b818736c444a)), closes [#123](https://github.com/mcp-hangar/mcp-hangar-operator/issues/123)
* **api:** remove MCPServerGroup spec.strategy and status.activeStrategy ([#128](https://github.com/mcp-hangar/mcp-hangar-operator/issues/128)) ([a6a68bf](https://github.com/mcp-hangar/mcp-hangar-operator/commit/a6a68bfb816d114f57d6f1aff9c427303d84e2f9)), closes [#122](https://github.com/mcp-hangar/mcp-hangar-operator/issues/122)
* **controller:** reconcilers and the pod builder speak v1alpha2 ([#132](https://github.com/mcp-hangar/mcp-hangar-operator/issues/132)) ([741c4d3](https://github.com/mcp-hangar/mcp-hangar-operator/commit/741c4d3cdfc6128ef6d28c60c8217cc6e5153694))
