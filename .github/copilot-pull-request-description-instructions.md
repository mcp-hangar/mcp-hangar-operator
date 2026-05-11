# Copilot PR Description Instructions

When generating a pull request description for this repository, follow the
project's PR template at `.github/PULL_REQUEST_TEMPLATE.md` exactly.

## Required structure

The PR body MUST contain these five `##` sections, in this exact wording
(case-sensitive), in any order:

1. `## Why` -- 1-3 sentences on motivation. Include `Closes #N` or `Refs #N`
   when the PR resolves or relates to an issue.
2. `## What` -- bulleted list of changes in imperative voice.
3. `## How tested` -- commands run, scenarios verified.
4. `## Risk and rollback` -- what could break and how to revert.
5. `## CHANGELOG note` -- paste the entry the PR adds, or write
   `skip-changelog: <reason>` when the change does not touch a triggering
   path (`api/`, `internal/`, `config/`, `go.mod`).

Optionally append `## Agent metadata` for agent-authored PRs.

## What NOT to do

- Do not invent extra sections.
- Do not omit any required section.
- Do not use emoji.
- Do not include attribution footers.
- Do not put body content inside HTML comments.
