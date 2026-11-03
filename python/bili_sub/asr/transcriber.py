"""In-process faster-whisper backend (no subprocess, no JSON protocol)."""

from __future__ import annotations

import os
import sys
import sysconfig
from pathlib import Path

from ..types import Segment, round_ms

DEFAULT_MODEL = "small"


class EngineError(RuntimeError):
    """The model could not be loaded (as opposed to one unreadable audio file)."""


_CUDA_REGISTERED = False


def _register_cuda_dlls() -> None:
    global _CUDA_REGISTERED
    if os.name != "nt" or _CUDA_REGISTERED:
        return
    _CUDA_REGISTERED = True
    base = Path(sysconfig.get_paths().get("purelib", "")) / "nvidia"
    for sub in ("cublas/bin", "cudnn/bin"):
        path = base / sub
        if not path.is_dir():
            continue
        try:
            os.add_dll_directory(str(path))
        except (AttributeError, OSError):
            pass
        os.environ["PATH"] = str(path) + os.pathsep + os.environ.get("PATH", "")


def _batch_size() -> int:
    try:
        value = int(os.environ.get("ASR_BATCH_SIZE", ""))
    except ValueError:
        return 8
    return value if value > 0 else 8


def _build_pipeline(model):
    try:
        from faster_whisper import BatchedInferencePipeline
    except ImportError:
        return model
    return BatchedInferencePipeline(model=model)


class Transcriber:
    def __init__(self, model: str = "", simplify: bool = True):
        self.model_name = model or DEFAULT_MODEL
        self.simplify = simplify
        self._model = None
        self._loaded_name = ""
        self._device = ""
        self._cuda_proven = False

    @property
    def device(self) -> str:
        return self._device

    def _load(self, name: str, device: str):
        try:
            from faster_whisper import WhisperModel
        except ImportError as exc:
            raise EngineError(
                'faster-whisper 未安装：请用 `pip install -e ".[asr]"` '
                f"（{exc}）"
            ) from exc
        try:
            if device == "cuda":
                model = WhisperModel(name, device="cuda", compute_type="float16")
            else:
                model = WhisperModel(name, device="cpu", compute_type="int8")
        except Exception as exc:  # noqa: BLE001 - any load failure is engine-class
            raise EngineError(f"{type(exc).__name__}: {exc}") from exc
        self._model = model
        self._loaded_name = name
        self._device = device
        print(
            f"[asr] {name} on {'cuda/float16' if device == 'cuda' else 'cpu/int8'}",
            file=sys.stderr,
            flush=True,
        )
        return model

    def _model_for(self, name: str, device: str | None = None):
        device = device or self._device or "cuda"
        if self._model is None or name != self._loaded_name or device != self._device:
            return self._load(name, device)
        return self._model

    def _decode(self, model, audio: str, lang: str) -> list[Segment]:
        pipeline = _build_pipeline(model)
        kwargs = {"language": lang or None, "vad_filter": True}
        try:
            segments, _info = pipeline.transcribe(audio, batch_size=_batch_size(), **kwargs)
        except TypeError:
            # Older faster-whisper without batched inference also rejects batch_size.
            segments, _info = model.transcribe(audio, **kwargs)
        return [
            Segment(
                start_ms=round_ms(seg.start),
                end_ms=round_ms(seg.end),
                text=(seg.text or "").strip(),
            )
            for seg in segments
        ]

    def transcribe(self, audio: str | Path, lang: str = "", model: str = "") -> list[Segment]:
        _register_cuda_dlls()
        name = model or self.model_name or DEFAULT_MODEL
        device = self._device or "cuda"
        try:
            loaded = self._model_for(name, device)
            result = self._decode(loaded, str(audio), lang)
        except EngineError:
            if device != "cuda" or self._cuda_proven:
                raise
            print("[asr] CUDA 不可用，改用 CPU int8 重试", file=sys.stderr, flush=True)
            loaded = self._model_for(name, "cpu")
            result = self._decode(loaded, str(audio), lang)
        except Exception as exc:  # noqa: BLE001 - decode can fail on a broken CUDA install
            if device != "cuda" or self._cuda_proven:
                raise
            print(
                f"[asr] CUDA 不可用（{type(exc).__name__}: {exc}），改用 CPU int8 重试",
                file=sys.stderr,
                flush=True,
            )
            loaded = self._model_for(name, "cpu")
            result = self._decode(loaded, str(audio), lang)
        if self._device == "cuda":
            self._cuda_proven = True
        return result
