"""``.env`` configuration with the same keys and defaults as the Go version."""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path

from . import paths

_TRUE = {"1", "t", "true", "y", "yes", "on"}
_FALSE = {"0", "f", "false", "n", "no", "off"}

KNOWN_KEYS = (
    "SESSDATA",
    "BILI_DATA_DIR",
    "BILI_CONCURRENCY",
    "BILI_QPS",
    "FFMPEG_PATH",
    "PYTHON_PATH",
    "ASR_MODEL",
    "ASR_SIMPLIFY",
)


class ConfigError(ValueError):
    pass


@dataclass
class Config:
    sessdata: str = ""
    data_dir_raw: str = "data"
    data_dir: Path = field(default_factory=paths.default_data_dir)
    concurrency: int = 3
    qps: float = 2.0
    ffmpeg_path: str = "ffmpeg"
    python_path: str = "python"  # accepted for .env compatibility; the ASR runs in-process
    asr_model: str = ""
    asr_simplify: bool = True

    @property
    def db_path(self) -> Path:
        return self.data_dir / "bili_sub.db"

    @property
    def session_path(self) -> Path:
        return self.data_dir / "session.json"

    @property
    def subtitle_dir(self) -> Path:
        return self.data_dir / "subtitles"

    @property
    def audio_dir(self) -> Path:
        return self.data_dir / "audio"

    @property
    def export_dir(self) -> Path:
        return self.data_dir / "exports"


def default() -> Config:
    return Config(
        data_dir_raw="data",
        data_dir=paths.default_data_dir(),
    )


def _parse_bool(value: str) -> bool:
    low = value.strip().lower()
    if low in _TRUE:
        return True
    if low in _FALSE:
        return False
    raise ConfigError(f"invalid boolean: {value!r}")


def _set(cfg: Config, key: str, value: str) -> None:
    if key == "SESSDATA":
        cfg.sessdata = value
    elif key == "BILI_DATA_DIR":
        cfg.data_dir_raw = value
        cfg.data_dir = paths.resolve_data_dir(value)
    elif key == "BILI_CONCURRENCY":
        try:
            cfg.concurrency = int(value)
        except ValueError as exc:
            raise ConfigError(str(exc)) from exc
    elif key == "BILI_QPS":
        try:
            cfg.qps = float(value)
        except ValueError as exc:
            raise ConfigError(str(exc)) from exc
    elif key == "FFMPEG_PATH":
        cfg.ffmpeg_path = value
    elif key == "PYTHON_PATH":
        cfg.python_path = value
    elif key == "ASR_MODEL":
        cfg.asr_model = value
    elif key == "ASR_SIMPLIFY":
        cfg.asr_simplify = _parse_bool(value)
    else:
        raise ConfigError(f'未知配置项 "{key}"')


def load(env_path: str | os.PathLike[str] = ".env") -> Config:
    cfg = default()
    path = paths.resolve_env_path(str(env_path))
    try:
        text = Path(path).read_text(encoding="utf-8")
    except FileNotFoundError:
        text = ""
    except OSError as exc:
        raise ConfigError(f"读取 {path} 失败: {exc}") from exc

    for raw_line in text.splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        try:
            _set(cfg, key, value.strip())
        except ConfigError as exc:
            raise ConfigError(f"config {key}: {exc}") from exc

    for key in KNOWN_KEYS:
        value = os.environ.get(key)
        if value:
            try:
                _set(cfg, key, value)
            except ConfigError as exc:
                raise ConfigError(f"config {key}: {exc}") from exc
    return cfg
