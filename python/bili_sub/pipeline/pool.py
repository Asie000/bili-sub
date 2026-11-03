"""Minimal ordered worker pool (the Go version's pipeline.Run)."""

from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from typing import Callable, TypeVar

T = TypeVar("T")
R = TypeVar("R")


def run_ordered(
    items: list[T],
    workers: int,
    fn: Callable[[T], R],
) -> list[tuple[int, R | None, BaseException | None]]:
    if not items:
        return []
    workers = max(1, min(workers or 1, len(items)))
    results: list[tuple[int, R | None, BaseException | None]] = [
        (i, None, None) for i in range(len(items))
    ]

    def task(index: int, item: T) -> tuple[int, R | None, BaseException | None]:
        try:
            return index, fn(item), None
        except BaseException as exc:  # noqa: BLE001 - surfaced to the caller
            return index, None, exc

    with ThreadPoolExecutor(max_workers=workers, thread_name_prefix="bili-sub") as pool:
        futures = [pool.submit(task, i, item) for i, item in enumerate(items)]
        for future in futures:
            index, value, error = future.result()
            results[index] = (index, value, error)
    return results
