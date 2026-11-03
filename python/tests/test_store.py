from __future__ import annotations

from bili_sub.store import (
    STATE_EXPORTED,
    STATE_FETCHED,
    STATE_NO_SUBTITLE,
    STATE_PENDING,
    Store,
)
from bili_sub.types import Video


def _video(bvid="BV1", cid=1, state=STATE_PENDING, page=1) -> Video:
    return Video(
        id=0,
        bvid=bvid,
        cid=cid,
        aid=9,
        author="UP",
        title="标题",
        pubdate=0,
        duration=10,
        page_index=page,
        part="",
        is_charging=False,
        state=state,
        lang="",
        source_key="src",
    )


def test_upsert_is_idempotent_and_keeps_state(tmp_path):
    store = Store(tmp_path / "db.sqlite")
    first = store.upsert_video(_video())
    second = store.upsert_video(_video())
    assert first == second
    store.set_state(first, STATE_FETCHED, "zh")
    store.upsert_video(_video())
    rows = store.list_videos()
    assert len(rows) == 1
    assert rows[0].state == STATE_FETCHED
    assert rows[0].lang == "zh"
    store.close()


def test_filters_and_counts(tmp_path):
    store = Store(tmp_path / "db.sqlite")
    a = store.upsert_video(_video("BV1", 1))
    store.upsert_video(_video("BV2", 2))
    store.set_state(a, STATE_NO_SUBTITLE, "")
    assert store.count_videos() == 2
    assert store.count_videos(STATE_NO_SUBTITLE) == 1
    assert [v.bvid for v in store.list_videos_filtered(STATE_NO_SUBTITLE)] == ["BV1"]
    assert [v.bvid for v in store.list_videos_by_source("src")] == ["BV1", "BV2"]
    store.close()


def test_transcripts_and_export_marking(tmp_path):
    store = Store(tmp_path / "db.sqlite")
    vid = store.upsert_video(_video())
    store.save_transcript(vid, "bilibili", "zh", '{"bvid":"BV1"}')
    assert store.get_transcript(vid) == ("bilibili", "zh", '{"bvid":"BV1"}')
    store.save_transcript(vid, "faster-whisper", "zh", '{"bvid":"BV2"}')
    assert store.get_transcript(vid)[0] == "faster-whisper"
    store.mark_exported([vid])
    assert store.list_videos()[0].state == STATE_EXPORTED
    store.close()


def test_delete_all_is_transactional(tmp_path):
    store = Store(tmp_path / "db.sqlite")
    vid = store.upsert_video(_video())
    store.save_transcript(vid, "x", "zh", "{}")
    assert store.delete_all_videos() == 1
    assert store.list_videos() == []
    assert store.get_transcript(vid) is None
    store.close()
