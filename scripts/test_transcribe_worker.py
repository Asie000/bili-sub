#!/usr/bin/env python3
"""Unit tests for scripts/transcribe_worker.py.

The model is stubbed, so these need neither a GPU, nor faster-whisper, nor a
model download -- only a Python 3 interpreter. `go test ./internal/asr` runs
this file through `python test_transcribe_worker.py` and skips when no
interpreter is on PATH, which keeps the Go<->Python JSON-lines contract and the
CUDA->CPU downgrade pinned in the normal suite instead of only in a live run.

Run directly:  python scripts/test_transcribe_worker.py
"""

import importlib.util
import io
import json
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
WORKER_PATH = os.path.join(HERE, "transcribe_worker.py")


def load_worker():
    """Import transcribe_worker.py as a module without running main()."""
    spec = importlib.util.spec_from_file_location("transcribe_worker", WORKER_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class FakeSegment:
    def __init__(self, start, end, text):
        self.start = start
        self.end = end
        self.text = text


class WorkingModel:
    """A model whose transcribe() yields one padded segment."""

    def __init__(self, text="你好"):
        self.text = text

    def transcribe(self, audio, language=None, vad_filter=False):
        return iter([FakeSegment(0.0, 1.5, " " + self.text + " ")]), object()


class BrokenModel:
    """A model that fails the way ctranslate2 does when the CUDA runtime is
    missing: construction succeeds and the first decode raises."""

    def transcribe(self, audio, language=None, vad_filter=False):
        raise RuntimeError("Library cublas64_12.dll is not found or cannot be loaded")


class SelectiveModel:
    """Works for every file except one, which fails the way an undecodable or
    truncated download does."""

    def __init__(self, bad="bad.m4s"):
        self.bad = bad

    def transcribe(self, audio, language=None, vad_filter=False):
        if audio == self.bad:
            raise RuntimeError("Invalid data found when processing input")
        return iter([FakeSegment(0.0, 1.0, "ok")]), object()


def run_worker(module, requests):
    """Feed request dicts through module.main() and return the parsed replies."""
    payload = "".join(json.dumps(req) + "\n" for req in requests)
    return run_worker_raw(module, payload)


def run_worker_raw(module, payload):
    """Feed raw stdin text through module.main() and return the parsed replies."""
    stdin = io.StringIO(payload)
    stdout = io.StringIO()
    real_in, real_out = sys.stdin, sys.stdout
    sys.stdin, sys.stdout = stdin, stdout
    try:
        module.main()
    finally:
        sys.stdin, sys.stdout = real_in, real_out
    return [json.loads(line) for line in stdout.getvalue().splitlines() if line.strip()]


class WorkerProtocolTest(unittest.TestCase):
    """The wire shape the Go side decodes: internal/asr/worker.go."""

    def setUp(self):
        self.worker = load_worker()
        reset_model_cache(self.worker)

    def test_response_carries_exactly_the_keys_the_go_side_decodes(self):
        self.worker.build_model = lambda name, device: WorkingModel()

        replies = run_worker(self.worker, [{"audio": "a.m4s", "lang": "zh", "model": "tiny"}])

        self.assertEqual(len(replies), 1)
        self.assertEqual(set(replies[0]), {"segments", "error"})
        self.assertEqual(replies[0]["error"], "")
        self.assertEqual(set(replies[0]["segments"][0]), {"start_ms", "end_ms", "text"})
        self.assertEqual(
            replies[0]["segments"][0],
            {"start_ms": 0, "end_ms": 1500, "text": "你好"},
        )

    def test_one_reply_per_request_keeps_the_stream_in_step(self):
        self.worker.build_model = lambda name, device: WorkingModel()

        replies = run_worker(
            self.worker,
            [{"audio": "a.m4s", "lang": "zh"}, {"audio": "b.m4s", "lang": "en"}],
        )

        self.assertEqual(len(replies), 2)
        self.assertEqual([r["error"] for r in replies], ["", ""])

    def test_a_request_without_audio_is_answered_and_not_fatal(self):
        self.worker.build_model = lambda name, device: WorkingModel()

        replies = run_worker(self.worker, [{"lang": "zh"}, {"audio": "b.m4s", "lang": "zh"}])

        self.assertEqual(len(replies), 2)
        self.assertIn("audio", replies[0]["error"])
        self.assertEqual(replies[1]["error"], "")

    def test_unparsable_line_is_answered_and_does_not_stop_the_loop(self):
        self.worker.build_model = lambda name, device: WorkingModel()

        replies = run_worker_raw(
            self.worker, "{not json\n" + json.dumps({"audio": "b.m4s", "lang": "zh"}) + "\n"
        )

        self.assertEqual(len(replies), 2)
        self.assertIn("JSON", replies[0]["error"])
        self.assertEqual(replies[1]["error"], "")


class ModelCacheTest(unittest.TestCase):
    """The worker promises to load the model once (spec §九), GPU or CPU."""

    def setUp(self):
        self.worker = load_worker()
        reset_model_cache(self.worker)

    def test_repeated_requests_build_the_model_once(self):
        builds = []

        def build(name, device):
            builds.append(device)
            return WorkingModel()

        self.worker.build_model = build

        run_worker(self.worker, [{"audio": "a.m4s"}, {"audio": "b.m4s"}, {"audio": "c.m4s"}])

        self.assertEqual(builds, ["cuda"])

    def test_inference_failure_falls_back_to_cpu_and_sticks(self):
        builds = []

        def build(name, device):
            builds.append(device)
            return BrokenModel() if device == "cuda" else WorkingModel()

        self.worker.build_model = build

        replies = run_worker(self.worker, [{"audio": "a.m4s"}, {"audio": "b.m4s"}])

        self.assertEqual([r["error"] for r in replies], ["", ""])
        # One failed CUDA attempt for the whole process, then CPU for both
        # requests: a downgrade that is re-attempted per request would load two
        # models every time and re-run a doomed GPU inference.
        self.assertEqual(builds, ["cuda", "cpu"])

    def test_construction_failure_falls_back_to_cpu_too(self):
        """WhisperModel(...) itself can raise on a broken CUDA install, not just
        the first decode, so the fallback has to cover the load as well."""
        builds = []

        def build(name, device):
            builds.append(device)
            if device == "cuda":
                raise RuntimeError("Library cublas64_12.dll is not found or cannot be loaded")
            return WorkingModel()

        self.worker.build_model = build

        replies = run_worker(self.worker, [{"audio": "a.m4s"}])

        self.assertEqual(replies[0]["error"], "")
        self.assertEqual(builds, ["cuda", "cpu"])

    def test_cpu_failure_is_reported_without_retrying_the_gpu(self):
        builds = []

        def build(name, device):
            builds.append(device)
            return BrokenModel() if device == "cuda" else BrokenModel()

        self.worker.build_model = build

        replies = run_worker(self.worker, [{"audio": "a.m4s"}])

        self.assertIn("cublas", replies[0]["error"])
        self.assertEqual(builds, ["cuda", "cpu"])

    def test_a_file_specific_failure_after_cuda_worked_does_not_stick_to_cpu(self):
        """Only a GPU that has never produced a result may be treated as broken.

        Once CUDA has transcribed something, a single undecodable download says
        nothing about the device -- and permanently switching the rest of the
        batch to cpu/int8 costs about 20x (GPU 24s vs CPU 8m16s for the same
        clip), for a failure the CPU retry cannot fix either.
        """
        builds = []

        def build(name, device):
            builds.append(device)
            return SelectiveModel()

        self.worker.build_model = build

        replies = run_worker(
            self.worker,
            [{"audio": "good.m4s"}, {"audio": "bad.m4s"}, {"audio": "good2.m4s"}],
        )

        self.assertEqual(replies[0]["error"], "")
        self.assertIn("Invalid data", replies[1]["error"])
        self.assertEqual(replies[2]["error"], "")
        self.assertEqual(builds, ["cuda"])
        self.assertEqual(self.worker._device, "cuda")


class StdioEncodingTest(unittest.TestCase):
    """Stdio encoding is a real failure mode on Windows: the fallback message is
    Chinese and is printed from inside the except block that guards the CPU
    retry, so a code page that cannot encode it would abort the retry it is
    announcing."""

    def test_chinese_diagnostics_survive_a_legacy_code_page(self):
        worker = load_worker()
        legacy = io.TextIOWrapper(io.BytesIO(), encoding="cp1252", errors="strict")
        real = sys.stderr
        sys.stderr = legacy
        try:
            worker.force_utf8_stdio()
            legacy.write("CUDA 不可用，改用 CPU int8 重试\n")
            legacy.flush()
        finally:
            sys.stderr = real


def reset_model_cache(module):
    """Drop the module's cached model so one test cannot leak into the next."""
    module._model = None
    module._model_name = None
    module._device = None
    module._cuda_proven = False


if __name__ == "__main__":
    unittest.main(verbosity=2)
