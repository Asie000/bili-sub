from __future__ import annotations

from fastapi.testclient import TestClient

from bili_sub.config import Config
from bili_sub.runtime import Runtime
from bili_sub.server.app import create_app
from bili_sub.store import Store
from bili_sub.types import Video


def _runtime(tmp_path) -> Runtime:
    cfg = Config(data_dir_raw=str(tmp_path), data_dir=tmp_path)
    rt = Runtime(cfg)
    store = rt.store()
    store.upsert_video(
        Video(
            id=0,
            bvid="BV1",
            cid=1,
            aid=2,
            author="UP",
            title="标题",
            pubdate=0,
            duration=1,
            page_index=1,
            part="",
            is_charging=False,
            state="pending",
            lang="",
            source_key="s",
        )
    )
    return rt


def test_videos_api_uses_go_field_names(tmp_path):
    rt = _runtime(tmp_path)
    client = TestClient(create_app(rt))
    data = client.get("/api/videos").json()
    assert data["total"] == 1
    assert data["items"][0]["Bvid"] == "BV1"
    assert data["items"][0]["State"] == "pending"
    assert client.get("/api/stats").json() == {"pending": 1}
    rt.close()


def test_post_requires_json_content_type(tmp_path):
    rt = _runtime(tmp_path)
    client = TestClient(create_app(rt))
    resp = client.post("/api/reset", content=b'{"confirm":true}')
    assert resp.status_code == 400
    assert "application/json" in resp.json()["error"]
    rt.close()


def test_reset_requires_confirm_and_clears_rows(tmp_path):
    rt = _runtime(tmp_path)
    client = TestClient(create_app(rt))
    assert client.post("/api/reset", json={"confirm": False}).status_code == 400
    resp = client.post("/api/reset", json={"confirm": True})
    assert resp.status_code == 200
    assert resp.json()["deleted"] == 1
    assert rt.store().list_videos() == []
    rt.close()


def test_videos_pagination(tmp_path):
    rt = _runtime(tmp_path)
    store = rt.store()
    for i in range(5):
        store.upsert_video(
            Video(
                id=0,
                bvid=f"BX{i}",
                cid=i,
                aid=i,
                author="UP",
                title=f"t{i}",
                pubdate=0,
                duration=1,
                page_index=1,
                part="",
                is_charging=False,
                state="pending",
                lang="",
                source_key="s",
            )
        )
    client = TestClient(create_app(rt))
    page = client.get("/api/videos?offset=1&limit=2").json()
    assert page["total"] == 6
    assert [v["Bvid"] for v in page["items"]] == ["BX0", "BX1"]
    rt.close()
