"""Character-level traditional -> simplified folding.

The table was generated from zhconv (MIT), the same source the Go version used,
and ships with the package so the ASR extra does not need another dependency.
"""

from __future__ import annotations

import json
from pathlib import Path

_TABLE_PATH = Path(__file__).with_name("data") / "t2s.json"
try:
    TRAD_TO_SIMP: dict[str, str] = json.loads(_TABLE_PATH.read_text(encoding="utf-8"))
except (OSError, ValueError):
    TRAD_TO_SIMP = {}


def to_simplified(text: str) -> str:
    if not TRAD_TO_SIMP:
        return text
    return "".join(TRAD_TO_SIMP.get(ch, ch) for ch in text)
