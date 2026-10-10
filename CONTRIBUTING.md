# Contributing

Thank you for your interest in contributing to MCP Hangar Operator!

## Quick Start

```bash
git clone https://github.com/mcp-hangar/mcp-hangar-operator.git
cd mcp-hangar-operator

# Generate CRDs and DeepCopy
make manifests
make generate

# Build
go build ./cmd/...

# Test
make test

# Lint (the golangci-lint v2 version the Makefile pins, installed into bin/)
make lint
```

## Licensing

MCP Hangar Operator is licensed under the [MIT License](LICENSE).

## Code of Conduct

Please read our [Code of Conduct](CODE_OF_CONDUCT.md) before contributing.
