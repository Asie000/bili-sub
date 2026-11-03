#!/usr/bin/env python3
"""Long-running JSON-lines ASR worker for bili-sub.

The Go control plane keeps one such process alive for a whole download batch and
exchanges one request per line on stdin, one response per line on stdout:

    request : {"audio": "<path>", "lang": "zh", "model": "small"}
    response: {"segments": [{"start_ms": 0, "end_ms": 1500, "text": "..."}], "error": ""}

`lang` may be omitted/empty to let Whisper auto-detect; `model` may be omitted
to use the default. The model is loaded ONCE (lazily, on the first request that
needs it) and reused for every following line, because loading costs hundreds of
MB and several seconds.

An empty `error` field means success; otherwise the message is passed back
verbatim. The loop never exits on a request-level failure (including a missing
dependency) so one bad file -- or a missing faster-whisper install -- cannot
leave a Go-side request hanging.

Requirements (see README / spec section 4):
    * Python 3.9-3.12 -- 3.13+ usually has no prebuilt faster-whisper wheels yet,
      so use e.g. `uv venv --python 3.11` if the system Python is newer.
    * `pip install faster-whisper "av<19"` -- av 19 removed the
      `metadata_errors` argument faster-whisper 1.2.1 still passes, and without
      the pin the first decode fails with a TypeError.
    * no ffmpeg needed: faster-whisper decodes through PyAV, so the dash .m4s is
      handed straight to transcribe().
    * a GPU is optional: with nvidia-cublas-cu12 / nvidia-cudnn-cu12 installed
      in this environment the worker uses cuda/float16, otherwise it falls back
      to cpu/int8 on its own.
"""

import json
import os
import sys

DEFAULT_MODEL = "small"

# BATCH_SIZE is how many 30s windows the batched pipeline decodes at once. With
# one window at a time the GPU sat around 40% busy; this is the knob that makes
# it worthwhile. 8 is faster-whisper's own default and fits an 8GB card with the
# large-v3-turbo weights (~1.6GB) loaded.
#
# ASR_BATCH_SIZE overrides it for a machine that runs out of VRAM at 8; it is a
# real environment variable rather than a .env key because the Go side launches
# this script as a child process, which inherits the shell's environment.
def _batch_size():
    try:
        n = int(os.environ.get("ASR_BATCH_SIZE", ""))
    except ValueError:
        return 8
    return n if n > 0 else 8


BATCH_SIZE = _batch_size()


def register_cuda_dlls():
    """Make pip-installed CUDA runtime DLLs (nvidia-*-cu12) findable.

    ctranslate2 loads cuBLAS/cuDNN lazily, once inference starts, and on Windows
    it resolves them through the process search path -- so the wheels' bin
    directories must be registered before the first transcribe call. Without
    this, a machine with a perfectly good NVIDIA card still dies with
    "cublas64_12.dll is not found", and the only fix is a PATH edit outside the
    program. Installing nvidia-cublas-cu12 into this environment is then enough.
    """
    if sys.platform != "win32":
        return
    for sub in ("cublas", "cudnn"):
        root = os.path.join(sys.prefix, "Lib", "site-packages", "nvidia", sub, "bin")
        if not os.path.isdir(root):
            continue
        os.environ["PATH"] = root + os.pathsep + os.environ.get("PATH", "")
        try:
            os.add_dll_directory(root)
        except OSError:
            pass


def force_utf8_stdio():
    """Pin the pipes to UTF-8 regardless of the OS locale.

    On Windows the stdio encoding follows the active code page (e.g. cp936 on
    zh-CN), so Chinese audio paths arriving from Go would be mojibake and the
    segment text written back would be unreadable. The Go side always speaks
    UTF-8, so both protocol directions are reconfigured once at startup.

    stderr carries diagnostics rather than the protocol, and it is pinned too:
    the CUDA->CPU fallback message is Chinese and is printed from inside the
    except block that guards the retry, so on a code page that cannot encode it
    the message itself would raise and the retry would never happen. For the
    same reason its errors are replaced instead of raised. Guarded because
    reconfigure only exists on TextIOWrapper (and the stream may be None).
    """
    for stream in (sys.stdin, sys.stdout):
        if hasattr(stream, "reconfigure"):
            stream.reconfigure(encoding="utf-8")
    if hasattr(sys.stderr, "reconfigure"):
        sys.stderr.reconfigure(encoding="utf-8", errors="replace")


# Loaded model cache: loading is expensive, so one WhisperModel is reused until
# a request asks for a different model (or the device had to be downgraded).
_model = None
_model_name = None
_device = None
# True once the GPU has actually produced a transcription. Until then a failed
# CUDA attempt is evidence the CUDA install is unusable; afterwards a failure is
# about the file, and downgrading the whole batch for it would be wrong.
_cuda_proven = False


def respond(payload):
    """Write exactly one compact JSON line and flush it immediately."""
    sys.stdout.write(json.dumps(payload, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def build_model(name, device):
    """Load one WhisperModel for the given device. float16 is the GPU compute
    type; int8 is the fast one on CPU (float16 has no CPU kernel)."""
    from faster_whisper import WhisperModel

    if device == "cuda":
        return WhisperModel(name, device="cuda", compute_type="float16")
    return WhisperModel(name, device="cpu", compute_type="int8")


def load_model(name, device=None):
    """Return the cached model, (re)building it only when the model or the
    device actually changes.

    `device=None` means "keep the device already settled on, or try cuda first".
    That is what makes a CPU downgrade stick: with a hard "cuda" default, every
    later request would see the cached device differ, rebuild the broken GPU
    model, and fail over again -- two model loads and one doomed GPU inference
    per video instead of one load for the whole run.
    """
    global _model, _model_name, _device
    device = device or _device or "cuda"
    if _model is None or name != _model_name or device != _device:
        _model = build_model(name, device)
        _model_name = name
        _device = device
        print("[worker] %s on %s" % (name, "cuda/float16" if device == "cuda" else "cpu/int8"),
              file=sys.stderr)
    return _model


def transcribe_segments(model, audio, lang):
    """Decode audio and return plain segment dicts. transcribe() returns a
    generator, so this loop is where ctranslate2 actually runs the model --
    and therefore where a broken CUDA install surfaces.

    The batched pipeline decodes several 30s windows at once instead of one,
    which is the difference between a GPU that sits near idle and one that is
    actually used. It is built per call because wrapping an already-loaded model
    is just a reference copy; there is no model to reload here.
    """
    # vad_filter drops leading/trailing silence so segment timestamps stay tight.
    segments, _info = build_pipeline(model).transcribe(
        audio, language=lang, vad_filter=True, batch_size=BATCH_SIZE
    )
    return [
        {
            "start_ms": int(round(seg.start * 1000)),
            "end_ms": int(round(seg.end * 1000)),
            "text": seg.text.strip(),
        }
        for seg in segments
    ]


class _DirectPipeline:
    """Pass-through used when the installed faster-whisper predates
    BatchedInferencePipeline: it keeps the one-window-at-a-time behaviour rather
    than failing, so batching is a speed-up and never a hard requirement."""

    def __init__(self, model):
        self.model = model

    def transcribe(self, audio, language=None, vad_filter=False, batch_size=None):
        return self.model.transcribe(audio, language=language, vad_filter=vad_filter)


def build_pipeline(model):
    """Wrap a loaded model in faster-whisper's batched pipeline."""
    try:
        from faster_whisper import BatchedInferencePipeline
    except ImportError:
        return _DirectPipeline(model)
    return BatchedInferencePipeline(model=model)


class EngineError(RuntimeError):
    """The engine could not do its job -- a model that cannot be loaded -- as
    opposed to an audio file it could not decode. Go stops the whole run on it
    instead of downloading every remaining video's audio only to fail the same
    way."""


def _build(name, device):
    """Load a model, tagging a load failure as an engine problem."""
    try:
        return load_model(name, device)
    except ImportError:
        raise
    except Exception as exc:  # noqa: BLE001 - any load failure is engine-class
        raise EngineError("%s: %s" % (type(exc).__name__, exc)) from exc


def transcribe_with_fallback(name, audio, lang):
    """Load (or reuse) the model and transcribe one file, retrying once on the
    CPU when the GPU attempt fails.

    A load failure and the first decode both sit behind the CUDA->CPU retry
    because a missing CUDA runtime surfaces at either point depending on the
    ctranslate2 build. Once CUDA has produced a result, a later failure is about
    that file, and switching the rest of the batch to cpu/int8 for it would cost
    ~20x while failing the same way -- so the retry is limited to a GPU that has
    never worked. A load failure that survives the retry is an EngineError: Go
    stops the run rather than paying for one audio download per remaining video.
    """
    global _cuda_proven
    device = _device or "cuda"
    try:
        model = _build(name, device)
    except EngineError as exc:
        if device != "cuda" or _cuda_proven:
            raise
        print("[worker] CUDA 不可用（%s），改用 CPU int8 重试" % exc, file=sys.stderr)
        model = _build(name, "cpu")
        return transcribe_segments(model, audio, lang)

    try:
        out = transcribe_segments(model, audio, lang)
    except ImportError:
        raise
    except Exception as exc:
        if device != "cuda" or _cuda_proven:
            raise
        print("[worker] CUDA 不可用（%s: %s），改用 CPU int8 重试"
              % (type(exc).__name__, exc), file=sys.stderr)
        model = _build(name, "cpu")
        return transcribe_segments(model, audio, lang)
    if device == "cuda":
        _cuda_proven = True
    return out


def main():
    force_utf8_stdio()
    register_cuda_dlls()
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except json.JSONDecodeError as exc:
            respond({"error": "请求不是合法 JSON: %s" % exc})
            continue

        audio = req.get("audio") or ""
        lang = req.get("lang") or None
        want_model = req.get("model") or DEFAULT_MODEL

        if not audio:
            respond({"error": "请求缺少 audio 字段"})
            continue

        try:
            out = transcribe_with_fallback(want_model, audio, lang)
            respond({"segments": out, "error": ""})
        except ImportError as exc:
            respond({
                "error": "faster-whisper 未安装：请在 Python 3.9-3.12 环境运行 "
                         "`pip install faster-whisper`（%s）" % exc,
                "kind": "engine",
            })
        except EngineError as exc:
            # About the engine, not the file: Go stops the run on this.
            respond({"error": str(exc), "kind": "engine"})
        except Exception as exc:  # noqa: BLE001 - a bad request must not kill the loop
            respond({"error": "%s: %s" % (type(exc).__name__, exc)})

    return 0


if __name__ == "__main__":
    sys.exit(main())
