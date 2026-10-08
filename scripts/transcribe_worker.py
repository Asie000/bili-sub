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
    * `pip install faster-whisper`
    * an ffmpeg build on PATH (faster-whisper decodes audio through it)
"""

import json
import sys

DEFAULT_MODEL = "small"

# Loaded model cache: loading is expensive, so one WhisperModel is reused until
# a request asks for a different model name.
_model = None
_model_name = None


def respond(payload):
    """Write exactly one compact JSON line and flush it immediately."""
    sys.stdout.write(json.dumps(payload, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def load_model(name):
    global _model, _model_name
    if _model is None or name != _model_name:
        from faster_whisper import WhisperModel
        _model = WhisperModel(name)
        _model_name = name
    return _model


def main():
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
            model = load_model(want_model)
            # vad_filter drops leading/trailing silence so segment timestamps
            # stay tight; transcribe returns a generator, so this loop is where
            # the actual decoding happens.
            segments, _info = model.transcribe(audio, language=lang, vad_filter=True)
            out = []
            for seg in segments:
                out.append({
                    "start_ms": int(round(seg.start * 1000)),
                    "end_ms": int(round(seg.end * 1000)),
                    "text": seg.text.strip(),
                })
            respond({"segments": out, "error": ""})
        except ImportError as exc:
            respond({
                "error": "faster-whisper 未安装：请在 Python 3.9-3.12 环境运行 "
                         "`pip install faster-whisper`（%s）" % exc,
            })
        except Exception as exc:  # noqa: BLE001 - a bad request must not kill the loop
            respond({"error": "%s: %s" % (type(exc).__name__, exc)})

    return 0


if __name__ == "__main__":
    sys.exit(main())
