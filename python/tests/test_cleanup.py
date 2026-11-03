from __future__ import annotations

from bili_sub.asr.cleanup import clean_segments, clean_text, is_garbled
from bili_sub.asr.t2s import to_simplified
from bili_sub.types import Segment


def test_clean_text_removes_tokens_and_cjk_spaces():
    assert clean_text("<|0.00|>你 好 世界") == "你好世界"
    assert clean_text("用 Go 写") == "用 Go 写"
    assert clean_text("\ufffd你\ufffd好") == "你好"


def test_traditional_to_simplified():
    assert to_simplified("我們這樣") == "我们这样"


def test_garbled_detection():
    assert is_garbled("\ufffd\ufffd\ufffd")
    assert not is_garbled("你好\ufffd")
    assert is_garbled("\ufffd" * 4)


def test_clean_segments_dedupes_consecutive_lines():
    segs = [
        Segment(0, 1000, " 今天 天气 不错 "),
        Segment(1000, 2000, "今天天气不错"),
        Segment(2000, 3000, "嗯"),
        Segment(3000, 4000, "嗯"),
        Segment(4000, 5000, "   "),
    ]
    out = clean_segments(segs, "zh", True)
    # The long line collapses, but a repeated single character is real speech.
    assert [s.text for s in out] == ["今天天气不错", "嗯", "嗯"]


def test_simplify_only_for_chinese():
    segs = [Segment(0, 1000, "我們")]
    assert clean_segments(segs, "en", True)[0].text == "我們"
    assert clean_segments(segs, "zh-CN", True)[0].text == "我们"
