from __future__ import annotations

from bili_sub.bili.wbi import WBISigner, wbi_key


def test_mixin_key_and_known_signature():
    signer = WBISigner(
        "7cd084941338484aae1ad9425b84077c",
        "4932caff0ff746eab6f01bf08b70ac45",
    )
    assert signer.mixin_key == "ea1db124af3c7062474693fa704f4ff8"
    params = {"foo": "114", "bar": "514", "wts": "1700000000"}
    out = signer.sign(params)
    assert out["w_rid"] == "b087ee77bc14a2c3f19eb6ab9d167c5f"
    assert "w_rid" not in params


def test_signature_strips_javascript_unescaped_chars():
    signer = WBISigner(
        "7cd084941338484aae1ad9425b84077c",
        "4932caff0ff746eab6f01bf08b70ac45",
    )
    dirty = signer.sign({"keyword": "it's!", "wts": "1700000000"})
    clean = signer.sign({"keyword": "its", "wts": "1700000000"})
    assert dirty["w_rid"] == clean["w_rid"]
    assert dirty["keyword"] == "it's!"


def test_wbi_key_from_url():
    assert wbi_key("https://i0.hdslb.com/bfs/wbi/abc123.png") == "abc123"
