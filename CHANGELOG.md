# Changelog

## [0.17.1](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.0...v0.17.1) (2026-08-24)


### Added

* **api:** an egress policy can select on Mcp-Param-* headers ([#161](https://github.com/mcp-hangar/mcp-hangar-operator/issues/161)) ([5010359](https://github.com/mcp-hangar/mcp-hangar-operator/commit/5010359d19d6b5797dad67d64dc27644650ad653)), closes [#160](https://github.com/mcp-hangar/mcp-hangar-operator/issues/160)

## [0.17.0](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.16.0...v0.17.0) (2026-08-20)


### ⚠ BREAKING CHANGES

* **api:** MCPServer spec.resources takes resource.Quantity values in an open requests/limits map; spec.volumes is corev1.Volume and its mount moves to the new spec.volumeMounts; spec.securityContext is replaced by spec.podSecurityContext and spec.containerSecurityContext. See UPGRADE.md.

### Added

* **api:** embed the corev1 pod types on MCPServer instead of a lossy subset ([#153](https://github.com/mcp-hangar/mcp-hangar-operator/issues/153)) ([edcf782](https://github.com/mcp-hangar/mcp-hangar-operator/commit/edcf7827a54e3d0c695db551d9fe94c7605f1f22))
* **controller:** emit Events through events.k8s.io/v1, and turn SA1019 back on ([#150](https://github.com/mcp-hangar/mcp-hangar-operator/issues/150)) ([ca19ef8](https://github.com/mcp-hangar/mcp-hangar-operator/commit/ca19ef815585e87b55a26befadcfc19f41715f67)), closes [#58](https://github.com/mcp-hangar/mcp-hangar-operator/issues/58)
* **webhook:** warn when a CIDR egress rule cannot match in-cluster on Cilium ([#154](https://github.com/mcp-hangar/mcp-hangar-operator/issues/154)) ([63ca2d3](https://github.com/mcp-hangar/mcp-hangar-operator/commit/63ca2d38c075c8c6027c14b09a4344cae188e5d2)), closes [#152](https://github.com/mcp-hangar/mcp-hangar-operator/issues/152)


### Fixed

* **ci:** make lint runs the linter CI runs ([#155](https://github.com/mcp-hangar/mcp-hangar-operator/issues/155)) ([a4b388f](https://github.com/mcp-hangar/mcp-hangar-operator/commit/a4b388faec3fd711d8db90bab9e72e88d03e1738))

## [0.16.0](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.15.3...v0.16.0) (2026-08-17)


### ⚠ BREAKING CHANGES

* **api:** mcp-hangar.io/v1alpha1 manifests are no longer accepted; apply them as mcp-hangar.io/v1alpha2.

### Added

* **api:** stop serving v1alpha1 ([#136](https://github.com/mcp-hangar/mcp-hangar-operator/issues/136)) ([89c3a82](https://github.com/mcp-hangar/mcp-hangar-operator/commit/89c3a82db938cf7c32048ea4ad15c6278798de7e))

### Fixed

* **api:** delete the unserved v1alpha1 API, and port the wildcard-egress guard it was hiding ([#137](https://github.com/mcp-hangar/mcp-hangar-operator/issues/137)) ([f5c2041](https://github.com/mcp-hangar/mcp-hangar-operator/commit/f5c2041)) -- merged ahead of the release cut, so it ships in 0.16.0, not a follow-up


### Fixed

* **api:** delete the unserved v1alpha1 API, and port the wildcard-egress guard it was hiding ([#137](https://github.com/mcp-hangar/mcp-hangar-operator/issues/137)) ([f5c2041](https://github.com/mcp-hangar/mcp-hangar-operator/commit/f5c2041057064d60ab86ce0151fb28e45bc6ca7c))


### Changed

* **ci:** pre-1.0 breaking changes bump minor, and this release is 0.16.0 ([#139](https://github.com/mcp-hangar/mcp-hangar-operator/issues/139)) ([0562641](https://github.com/mcp-hangar/mcp-hangar-operator/commit/0562641faaadc6bff1cf1fa4c92574494b1e7e88))

## [0.15.3](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.15.2...v0.15.3) (2026-08-17)


### Fixed

* **api:** remove MCPServer idleTTL, healthCheck, and circuitBreaker ([#127](https://github.com/mcp-hangar/mcp-hangar-operator/issues/127)) ([317983e](https://github.com/mcp-hangar/mcp-hangar-operator/commit/317983e477e11e509af85f7e654b211293ef40f6)), closes [#120](https://github.com/mcp-hangar/mcp-hangar-operator/issues/120)
* **api:** remove MCPServer observability and unused capability declarations ([#130](https://github.com/mcp-hangar/mcp-hangar-operator/issues/130)) ([3b22f06](https://github.com/mcp-hangar/mcp-hangar-operator/commit/3b22f065191d5eaa90de63e26b7277b3e07fff00)), closes [#121](https://github.com/mcp-hangar/mcp-hangar-operator/issues/121)
* **api:** remove MCPServer spec.tools, which nothing enforced ([#126](https://github.com/mcp-hangar/mcp-hangar-operator/issues/126)) ([1600f8b](https://github.com/mcp-hangar/mcp-hangar-operator/commit/1600f8b4fe312279ac06b2677355f928ac4a35df)), closes [#119](https://github.com/mcp-hangar/mcp-hangar-operator/issues/119)
* **api:** remove MCPServerGroup failover, sessionAffinity, and circuitBreaker ([#129](https://github.com/mcp-hangar/mcp-hangar-operator/issues/129)) ([854a09a](https://github.com/mcp-hangar/mcp-hangar-operator/commit/854a09a93baf6ae06aef62d31f28b818736c444a)), closes [#123](https://github.com/mcp-hangar/mcp-hangar-operator/issues/123)
* **api:** remove MCPServerGroup spec.strategy and status.activeStrategy ([#128](https://github.com/mcp-hangar/mcp-hangar-operator/issues/128)) ([a6a68bf](https://github.com/mcp-hangar/mcp-hangar-operator/commit/a6a68bfb816d114f57d6f1aff9c427303d84e2f9)), closes [#122](https://github.com/mcp-hangar/mcp-hangar-operator/issues/122)
* **controller:** reconcilers and the pod builder speak v1alpha2 ([#132](https://github.com/mcp-hangar/mcp-hangar-operator/issues/132)) ([741c4d3](https://github.com/mcp-hangar/mcp-hangar-operator/commit/741c4d3cdfc6128ef6d28c60c8217cc6e5153694))
