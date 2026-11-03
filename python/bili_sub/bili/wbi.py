"""WBI request signing (B站's anti-scraping query signature)."""

from __future__ import annotations

import hashlib
import time
from urllib.parse import quote_plus

MIXIN_KEY_ENC_TAB = [
    46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49,
    33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40,
    61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11,
    36, 20, 34, 44, 52,
]


def _filter_value(value: str) -> str:
    return value.translate({ord(c): None for c in "!'()*"})


class WBISigner:
    def __init__(self, img_key: str, sub_key: str):
        raw = img_key + sub_key
        if len(raw) < 64:
            raise ValueError(f"wbi key too short: {len(raw)}")
        self.mixin_key = "".join(raw[i] for i in MIXIN_KEY_ENC_TAB)[:32]

    def sign(self, params: dict[str, str]) -> dict[str, str]:
        out = {k: str(v) for k, v in params.items()}
        out.setdefault("wts", str(int(time.time())))
        query = "&".join(
            f"{quote_plus(k)}={quote_plus(_filter_value(out[k]))}"
            for k in sorted(out)
        )
        out["w_rid"] = hashlib.md5((query + self.mixin_key).encode()).hexdigest()
        return out


def wbi_key(url: str) -> str:
    """https://i0.hdslb.com/bfs/wbi/abc.png -> abc"""
    base = url.rsplit("/", 1)[-1]
    if "." in base[1:]:
        return base.rsplit(".", 1)[0]
    return base
