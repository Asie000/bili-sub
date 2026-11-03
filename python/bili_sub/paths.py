"""Project-relative path resolution.

The Go version resolved ``.env``/``data`` against the working directory. This
port lives one level down (``python/``) but must still find the repository's
``.env`` and ``data/`` when run from either place, so a small marker-based
project root is used instead of hard-coding cwd.
"""

from __future__ import annotations

import os
from pathlib import Path

_MARKERS = (
    "scripts/transcribe_worker.py",
    "bili-sub.exe",
    "go.mod",
    "data/bili_sub.db",
)


def _looks_like_root(path: Path) -> bool:
    return any((path / marker).exists() for marker in _MARKERS)


def project_root() -> Path:
    """Find the directory that owns .env/data/, wherever the process was started.

    Order: the working directory when it is a project root, then an ancestor of
    the installed package that looks like one, then the package's own project
    directory (the one holding pyproject.toml). That last step is what makes
    double-clicking the console script (cwd = .venv/Scripts) still find data/.
    """
    cwd = Path.cwd()
    if _looks_like_root(cwd):
        return cwd

    package_dir = Path(__file__).resolve().parent
    for candidate in package_dir.parents:
        if _looks_like_root(candidate):
            return candidate

    project_dir = package_dir.parent
    if (project_dir / "pyproject.toml").exists():
        return project_dir
    return cwd


def resolve_env_path(env: str) -> Path:
    """A relative ``--env`` wins; the default ``.env`` falls back to the project root."""
    path = Path(env).expanduser()
    if path.is_absolute():
        return path
    if path.exists():
        return path
    root = project_root()
    candidate = root / path
    if candidate.exists():
        return candidate
    return path


def default_data_dir() -> Path:
    return project_root() / "data"


def resolve_data_dir(value: str) -> Path:
    path = Path(value).expanduser()
    if path.is_absolute():
        return path
    return project_root() / path


def check_reset_path(data_dir: str) -> None:
    """Refuse paths whose deletion would wipe the project, its parent or a root.

    The *raw* configured value is checked, before resolving it against the
    project root: resolving ``..`` first would hide the escape it represents.
    """
    cleaned = os.path.normpath(data_dir)
    if cleaned in ("", ".", "..", "/", "\\") or os.path.dirname(cleaned) == cleaned:
        raise ValueError(f'reset: 拒绝删除数据目录 "{data_dir}"')
    if cleaned.startswith(".." + os.sep):
        raise ValueError(f'reset: 拒绝删除数据目录 "{data_dir}"')
    drive, tail = os.path.splitdrive(cleaned)
    if drive and not tail:
        raise ValueError(f'reset: 拒绝删除数据目录 "{data_dir}"')
