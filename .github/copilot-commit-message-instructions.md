# Copilot Commit Message Instructions

When generating a commit message for this repository, follow Conventional
Commits with this project's exact type and scope vocabulary.

## Format

`<type>(<scope>): <subject>`

- `type` (required): one of `feat`, `fix`, `perf`, `refactor`, `docs`,
  `test`, `build`, `ci`, `chore`, `revert`, `security`.
- `scope` (required): one of `api`, `controller`, `webhook`, `health`,
  `config`, `ci`, `docs`, `deps`, `infra`, `tests`, `repo`.
  Empty scope is rejected.
- `subject`: imperative mood, total header length <=72 characters.
- `!` after scope (e.g., `feat(api)!: ...`) marks a breaking change.

## Subject rules

- Lowercase start preferred; sentence-case start acceptable.
- All-uppercase subjects forbidden.
- Mid-string uppercase characters are allowed: file names, acronyms.
- No trailing period.
- No emoji.

## Examples (accepted)

- `feat(api): add MCPProviderGroup v1alpha2 types`
- `fix(controller): handle deleted provider during reconcile`
- `docs(repo): update CODEOWNERS`
- `chore(deps): bump controller-runtime to v0.18`

## Examples (rejected)

- `Add MCPProviderGroup types` -- missing type and scope
- `feat: add types` -- missing required scope
- `feat(unknown): add types` -- scope not in allow-list
- A 73+ character header -- exceeds `header-max-length`
