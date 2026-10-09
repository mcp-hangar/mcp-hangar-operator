"""The changelog gate must not hold a trivial PR hostage.

`changelog.d/README.md` names the kinds that owe no fragment -- `chore(deps)`,
`ci`, `style`, `test`, pure `docs` -- and the gate implemented none of it. The
triggering paths are broad enough to catch all of them: a dependency bump edits
`pyproject.toml`, a test-only PR can add an extra, a CI change can touch a
workflow beside one. Each failed a REQUIRED check and waited for a human to
apply `skip-changelog`. These run the real script.

Ported from mcp-hangar core with the operator's repository, paths and titles.
"""

from __future__ import annotations

import pathlib
import subprocess

import pytest

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "check_changelog.sh"


def _git(repo: pathlib.Path, *args: str) -> str:
    return subprocess.run(["git", *args], cwd=repo, check=True, capture_output=True, text=True).stdout.strip()


@pytest.fixture
def repo(tmp_path: pathlib.Path) -> pathlib.Path:
    """A two-commit repo whose second commit edits a triggering path."""
    _git(tmp_path, "init", "-q", "-b", "main")
    _git(tmp_path, "config", "user.email", "test@example.com")
    _git(tmp_path, "config", "user.name", "test")
    (tmp_path / "go.mod").write_text("module x\n\ngo 1.25\n")
    _git(tmp_path, "add", "-A")
    _git(tmp_path, "commit", "-qm", "base")
    (tmp_path / "go.mod").write_text("module x\n\ngo 1.25\n\nrequire sigs.k8s.io/controller-runtime v0.23.0\n")
    _git(tmp_path, "add", "-A")
    _git(tmp_path, "commit", "-qm", "bump")
    return tmp_path


def _check(repo: pathlib.Path, title: str, labels: str = "") -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", str(SCRIPT)],
        cwd=repo,
        capture_output=True,
        text=True,
        env={
            "PATH": "/usr/bin:/bin:/usr/local/bin",
            "BASE_SHA": _git(repo, "rev-parse", "HEAD~1"),
            "HEAD_SHA": _git(repo, "rev-parse", "HEAD"),
            "PR_LABELS": labels,
            "PR_TITLE": title,
        },
    )


@pytest.mark.parametrize(
    "title",
    [
        "chore(deps): bump the kubernetes group with 4 updates",
        "ci(deps): bump github/codeql-action from 4.37.7 to 4.37.8 in the github-actions group",
        "build(deps-dev): bump ruff",
        # The other kinds changelog.d/README.md calls trivial. Each of these
        # can touch a triggering path: a test-only PR adding an extra, a CI
        # change beside a workflow, a docs PR editing a Go doc comment in `internal/`.
        "test(controller): fuzz the policy compiler",
        "ci(ci): pin every third-party action to a commit SHA",
        "docs(docs): explain the egress backstop",
        "style: reformat with gofmt",
    ],
)
def test_a_trivial_pr_needs_no_fragment(repo: pathlib.Path, title: str) -> None:
    result = _check(repo, title)

    assert result.returncode == 0, result.stdout + result.stderr
    assert "Changelog fragment not required" in result.stdout


def test_an_ordinary_change_to_a_triggering_path_still_owes_one(repo: pathlib.Path) -> None:
    """The gate is not weakened for anything but a bump."""
    result = _check(repo, "feat(api): add a thing")

    assert result.returncode == 1
    assert "No changelog fragment added" in result.stderr + result.stdout


@pytest.mark.parametrize(
    "title",
    [
        "feat(api): rework how deps are resolved",
        "fix(controller): the test harness was wrong",
        "refactor(controller): move the docs generator",
    ],
)
def test_a_real_change_naming_a_trivial_word_still_owes_one(repo: pathlib.Path, title: str) -> None:
    """The match is anchored on the Conventional Commit type, not on the words."""
    result = _check(repo, title)

    assert result.returncode == 1
