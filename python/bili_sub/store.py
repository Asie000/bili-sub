"""SQLite store: the exact schema and states the Go version writes."""

from __future__ import annotations

import sqlite3
import threading
import time
from pathlib import Path

from .types import Video

STATE_PENDING = "pending"
STATE_PROBED = "probed"
STATE_DOWNLOADABLE = "downloadable"
STATE_NO_SUBTITLE = "no_subtitle"
STATE_CHARGING_LOCKED = "charging_locked"
STATE_FETCHED = "fetched"
STATE_TRANSCRIBED = "transcribed"
STATE_EXPORTED = "exported"
STATE_FAILED = "failed"

SCHEMA = """
CREATE TABLE IF NOT EXISTS videos (
    id INTEGER PRIMARY KEY,
    bvid TEXT,
    cid INTEGER,
    aid INTEGER NOT NULL DEFAULT 0,
    author TEXT,
    title TEXT,
    pubdate INTEGER,
    duration INTEGER,
    page_index INTEGER,
    part TEXT,
    is_charging INTEGER NOT NULL DEFAULT 0,
    state TEXT,
    lang TEXT,
    source_key TEXT,
    UNIQUE(bvid,cid)
);

CREATE TABLE IF NOT EXISTS transcripts (
    id INTEGER PRIMARY KEY,
    video_id INTEGER NOT NULL UNIQUE,
    engine TEXT NOT NULL DEFAULT '',
    lang TEXT NOT NULL DEFAULT '',
    ir_json TEXT,
    created_at INTEGER
);
"""

_COLUMNS = (
    "id,bvid,cid,aid,author,title,pubdate,duration,page_index,part,"
    "is_charging,state,lang,source_key"
)


class Store:
    def __init__(self, path: str | Path):
        self.path = str(path)
        self._lock = threading.RLock()
        if self.path != ":memory:":
            Path(self.path).parent.mkdir(parents=True, exist_ok=True)
            self._db = sqlite3.connect(
                self.path, check_same_thread=False, timeout=5.0
            )
            self._db.execute("PRAGMA busy_timeout=5000")
            self._db.execute("PRAGMA journal_mode=WAL")
        else:
            self._db = sqlite3.connect(":memory:", check_same_thread=False)
        self._db.row_factory = sqlite3.Row
        with self._lock:
            self._db.executescript(SCHEMA)
            self._migrate()
            self._db.commit()

    def _migrate(self) -> None:
        try:
            self._db.execute(
                "ALTER TABLE videos ADD COLUMN aid INTEGER NOT NULL DEFAULT 0"
            )
            self._db.commit()
        except sqlite3.OperationalError as exc:
            if "duplicate column name" not in str(exc).lower():
                raise

    def close(self) -> None:
        with self._lock:
            self._db.close()

    # -- videos ---------------------------------------------------------

    def upsert_video(self, v: Video) -> int:
        with self._lock:
            self._db.execute(
                """
                INSERT INTO videos
                    (bvid,cid,aid,author,title,pubdate,duration,page_index,
                     part,is_charging,state,lang,source_key)
                VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
                ON CONFLICT(bvid,cid) DO UPDATE SET
                    aid=excluded.aid,
                    author=excluded.author, title=excluded.title,
                    pubdate=excluded.pubdate, duration=excluded.duration,
                    page_index=excluded.page_index, part=excluded.part,
                    is_charging=excluded.is_charging, source_key=excluded.source_key
                """,
                (
                    v.bvid,
                    v.cid,
                    v.aid,
                    v.author,
                    v.title,
                    v.pubdate,
                    v.duration,
                    v.page_index,
                    v.part,
                    1 if v.is_charging else 0,
                    v.state,
                    v.lang,
                    v.source_key,
                ),
            )
            self._db.commit()
            row = self._db.execute(
                "SELECT id FROM videos WHERE bvid=? AND cid=?", (v.bvid, v.cid)
            ).fetchone()
            return int(row["id"])

    def _query(self, where: str, args: tuple) -> list[Video]:
        with self._lock:
            rows = self._db.execute(
                f"SELECT {_COLUMNS} FROM videos{where} ORDER BY id", args
            ).fetchall()
        return [_row_to_video(r) for r in rows]

    def list_videos(self, state: str = "") -> list[Video]:
        if state:
            return self._query(" WHERE state=?", (state,))
        return self._query("", ())

    def list_videos_by_source(self, source: str) -> list[Video]:
        return self._query(" WHERE source_key=?", (source,))

    def list_videos_filtered(self, state: str = "", source: str = "") -> list[Video]:
        where, args = _filter(state, source)
        return self._query(where, args)

    def count_videos(self, state: str = "", source: str = "") -> int:
        where, args = _filter(state, source)
        with self._lock:
            row = self._db.execute(
                f"SELECT COUNT(*) AS n FROM videos{where}", args
            ).fetchone()
        return int(row["n"])

    def delete_all_videos(self) -> int:
        with self._lock:
            try:
                self._db.execute("DELETE FROM transcripts")
                cur = self._db.execute("DELETE FROM videos")
                self._db.commit()
                return int(cur.rowcount)
            except Exception:
                self._db.rollback()
                raise

    def set_state(self, video_id: int, state: str, lang: str = "") -> None:
        with self._lock:
            self._db.execute(
                "UPDATE videos SET state=?, lang=? WHERE id=?",
                (state, lang, video_id),
            )
            self._db.commit()

    def mark_exported(self, ids: list[int]) -> None:
        if not ids:
            return
        with self._lock:
            try:
                self._db.executemany(
                    "UPDATE videos SET state=? WHERE id=?",
                    [(STATE_EXPORTED, i) for i in ids],
                )
                self._db.commit()
            except Exception:
                self._db.rollback()
                raise

    # -- transcripts ----------------------------------------------------

    def save_transcript(self, video_id: int, engine: str, lang: str, ir_json: str) -> None:
        with self._lock:
            self._db.execute(
                """
                INSERT INTO transcripts (video_id,engine,lang,ir_json,created_at)
                VALUES (?,?,?,?,?)
                ON CONFLICT(video_id) DO UPDATE SET
                    engine=excluded.engine, lang=excluded.lang,
                    ir_json=excluded.ir_json, created_at=excluded.created_at
                """,
                (video_id, engine, lang, ir_json, int(time.time())),
            )
            self._db.commit()

    def get_transcript(self, video_id: int) -> tuple[str, str, str] | None:
        with self._lock:
            row = self._db.execute(
                "SELECT engine,lang,ir_json FROM transcripts WHERE video_id=?",
                (video_id,),
            ).fetchone()
        if row is None:
            return None
        return row["engine"], row["lang"], row["ir_json"]


def _filter(state: str, source: str) -> tuple[str, tuple]:
    clauses: list[str] = []
    args: list[str] = []
    if state:
        clauses.append("state=?")
        args.append(state)
    if source:
        clauses.append("source_key=?")
        args.append(source)
    if not clauses:
        return "", ()
    return " WHERE " + " AND ".join(clauses), tuple(args)


def _row_to_video(row: sqlite3.Row) -> Video:
    return Video(
        id=int(row["id"]),
        bvid=row["bvid"] or "",
        cid=int(row["cid"] or 0),
        aid=int(row["aid"] or 0),
        author=row["author"] or "",
        title=row["title"] or "",
        pubdate=int(row["pubdate"] or 0),
        duration=int(row["duration"] or 0),
        page_index=int(row["page_index"] or 1),
        part=row["part"] or "",
        is_charging=bool(row["is_charging"]),
        state=row["state"] or "",
        lang=row["lang"] or "",
        source_key=row["source_key"] or "",
    )
