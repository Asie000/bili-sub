"""A simple paced rate limiter with the same 2 req/s default and jitter."""

from __future__ import annotations

import random
import threading
import time


class RateLimiter:
    def __init__(self, qps: float):
        self.qps = qps if qps and qps > 0 else 2.0
        self._lock = threading.Lock()
        self._next = 0.0

    def wait(self) -> None:
        with self._lock:
            now = time.monotonic()
            if self._next < now:
                self._next = now
            self._next += 1.0 / self.qps
            delay = self._next - now
        if delay > 0:
            time.sleep(delay)
        time.sleep(random.random() * 0.2)
