# Security Policy

## Reporting a Vulnerability

We take the security of MCP Hangar seriously. If you believe you have found a security vulnerability, please report it to us as described below.

**Please do not report security vulnerabilities through public GitHub issues.**

### How to Report

1. **Email**: Send details to the project maintainers (contact information in the repository)
2. **Private Disclosure**: Use [GitHub's private vulnerability reporting](https://github.com/mcp-hangar/mcp-hangar-operator/security/advisories/new) if available

### What to Include

Please include the following information in your report:

- Type of vulnerability (e.g., privilege escalation, RBAC bypass, etc.)
- Full paths of source file(s) related to the vulnerability
- Step-by-step instructions to reproduce the issue
- Proof-of-concept or exploit code (if possible)
- Impact of the vulnerability and how it could be exploited

### Response Timeline

- **Initial Response**: Within 48 hours
- **Status Update**: Within 7 days
- **Resolution Target**: Within 30 days for critical issues

### What to Expect

1. Acknowledgment of your report
2. Assessment of the vulnerability
3. Development and testing of a fix
4. Coordinated disclosure timeline
5. Credit in the security advisory (unless you prefer to remain anonymous)

## Security Features

This project implements multiple security layers:

- **RBAC Generation**: Minimal ClusterRole/Role via kubebuilder markers
- **Admission Webhooks**: Validating and mutating webhooks for CRD resources
- **Pod Security Contexts**: Non-root, read-only filesystem, dropped capabilities
- **CRD Validation**: CEL and OpenAPI schema validation on custom resources

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 0.x     | :white_check_mark: |

## Acknowledgments

We appreciate responsible disclosure and will acknowledge security researchers who help improve our security.
