from __future__ import annotations

import pytest

from bili_sub.config import ConfigError, load


def _clear(monkeypatch):
    for key in (
        "SESSDATA",
        "BILI_DATA_DIR",
        "BILI_CONCURRENCY",
        "BILI_QPS",
        "FFMPEG_PATH",
        "PYTHON_PATH",
        "ASR_MODEL",
        "ASR_SIMPLIFY",
    ):
        monkeypatch.delenv(key, raising=False)


def test_defaults(tmp_path, monkeypatch):
    _clear(monkeypatch)
    cfg = load(tmp_path / "missing.env")
    assert cfg.concurrency == 3
    assert cfg.qps == 2.0
    assert cfg.asr_simplify is True
    assert cfg.data_dir.name == "data"


def test_reads_file_and_env_overrides(tmp_path, monkeypatch):
    _clear(monkeypatch)
    env = tmp_path / "x.env"
    env.write_text(
        "BILI_QPS=5\nBILI_CONCURRENCY=7\nASR_MODEL=tiny\nASR_SIMPLIFY=0\n",
        encoding="utf-8",
    )
    cfg = load(env)
    assert (cfg.qps, cfg.concurrency, cfg.asr_model, cfg.asr_simplify) == (
        5.0,
        7,
        "tiny",
        False,
    )
    monkeypatch.setenv("BILI_QPS", "9")
    assert load(env).qps == 9.0


def test_unknown_key_is_an_error(tmp_path, monkeypatch):
    _clear(monkeypatch)
    env = tmp_path / "x.env"
    env.write_text("BILI_QPP=5\n", encoding="utf-8")
    with pytest.raises(ConfigError, match="未知配置项"):
        load(env)


def test_bad_number_is_an_error(tmp_path, monkeypatch):
    _clear(monkeypatch)
    env = tmp_path / "x.env"
    env.write_text("BILI_QPS=fast\n", encoding="utf-8")
    with pytest.raises(ConfigError):
        load(env)
