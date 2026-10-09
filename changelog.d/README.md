# Changelog fragments

Every non-trivial PR drops **one new file** here instead of editing
`CHANGELOG.md`. At release time `scripts/build_changelog.py` folds them into a
single version section and deletes them.

This exists for one reason: a file every open PR writes to at the same spot
makes any two PRs open at once conflict. A fragment is a new file, so there is
nothing for git to merge. The mechanism is mcp-hangar core's, ported as is.

## Naming

```text
changelog.d/<id>-<slug>.<kind>.md
```

- `<id>` -- the issue or PR number. It is a sort key and a fallback link only:
  the PR link is normally read from the squash commit that added the file, so
  guessing the number wrong (or omitting it) does not produce a wrong link.
- `<slug>` -- a few words, kebab-case.
- `<kind>` -- one of `added`, `changed`, `deprecated`, `removed`, `fixed`,
  `security`. These are the Keep a Changelog sections and become the `###`
  headings, in that order.

```text
changelog.d/235-configmap-source-namespace.fixed.md
changelog.d/236-drop-legacy-backstop.removed.md
```

## Content

The file holds the entry text and nothing else -- no leading bullet, no heading,
no PR link. The assembler adds all three. Start with the Conventional Commit
scope in bold, matching the existing entries:

```markdown
**controller:** refuse a ConfigMap discovery source that references another
namespace. It used to read the ConfigMap with the operator's cluster-wide
access; see UPGRADE.md for how to move such a source
```

The scopes are the ones `pr-title` accepts: `api`, `ci`, `config`,
`controller`, `deps`, `docs`, `health`, `infra`, `tests`, `webhook`, `repo`.

Write it for a reader upgrading, not for a reviewer: what changed, what breaks,
what to do about it. A multi-paragraph fragment is fine -- continuation lines
are indented into the bullet automatically.

A breaking change is described here **and** gets an upgrade note in
`upgrade.d/` naming the old and new form (see `upgrade.d/README.md`).

## Release summary

An optional `changelog.d/_summary.md` becomes the intro paragraph above the
sections. Use it when a release has a theme worth stating in two sentences;
skip it otherwise.

## Trivial changes

`chore(deps)`, `ci`, `style`, `test` and pure `docs` PRs need no fragment. The
`changelog / check` gate only fires on changes under `api/`, `cmd/`,
`internal/`, `pkg/`, `config/`, or to `go.mod`, `go.sum` or `Dockerfile`, and
it reads the PR title to recognise those kinds -- a dependency bump edits
`go.mod`, a test-only PR adds a `_test.go` under `internal/`, so without that
each of them would fail the check and wait for a hand-applied label. The
`skip-changelog` label still bypasses it for anything the title does not cover.

Lifting the requirement is not the same as forbidding an entry: a `test` or
`ci` PR that a reader upgrading needs to know about may still add a fragment.

## Commands

```bash
python3 scripts/build_changelog.py check                     # validate every pending fragment
python3 scripts/build_changelog.py preview --version 0.18.0  # render the section to stdout
```

Assembly runs automatically on the release-please branch; it is not something
to do by hand on a feature branch.
