"""SSE event fan-out (the Go server's Hub)."""

from __future__ import annotations

import queue
import threading

BUFFER = 16


class Hub:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._subs: set[queue.Queue] = set()
        self._closed = False

    def subscribe(self) -> tuple[queue.Queue, callable]:
        q: queue.Queue = queue.Queue(maxsize=BUFFER)
        with self._lock:
            if self._closed:
                q.put_nowait(None)
                return q, lambda: None
            self._subs.add(q)

        def cancel() -> None:
            with self._lock:
                self._subs.discard(q)
            try:
                q.put_nowait(None)
            except queue.Full:
                pass

        return q, cancel

    def broadcast(self, event: dict) -> None:
        with self._lock:
            subs = list(self._subs)
        for q in subs:
            try:
                q.put_nowait(event)
            except queue.Full:
                # A slow browser is dropped from, never allowed to block a run.
                pass

    def close(self) -> None:
        with self._lock:
            self._closed = True
            subs = list(self._subs)
            self._subs.clear()
        for q in subs:
            try:
                q.put_nowait(None)
            except queue.Full:
                pass
