from __future__ import annotations

import os

import pytest

from bili_sub.paths import check_reset_path, resolve_env_path


def test_reset_guard_rejects_escapes():
    for value in ("..", ".", "/", os.path.normpath("../..")):
        with pytest.raises(ValueError, match="拒绝删除"):
            check_reset_path(value)


def test_reset_guard_accepts_project_data(tmp_path):
    check_reset_path(str(tmp_path / "data"))


def test_env_path_absolute_is_untouched(tmp_path):
    target = tmp_path / "custom.env"
    assert resolve_env_path(str(target)) == target
