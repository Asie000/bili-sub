"""Orchestration shared by the CLI and the web server."""

from __future__ import annotations

import json
import queue
import shutil
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, TextIO

from . import export as export_mod
from . import paths
from .asr import EngineError, Transcriber, clean_segments
from .bili import APIError, Client, RateLimiter, RiskControlError
from .bili.login import (
    extract_sessdata,
    load_session,
    save_session,
)
from .config import Config, load as load_config
from .filename import page_stem
from .pipeline import urlparse
from .pipeline.audio import AudioFetcher
from .pipeline.download import (
    ChargingLocked,
    Downloader,
    NoSubtitle,
    NoTrackForLang,
)
from .pipeline.enumerate import DEFAULT_SEARCH_LIMIT, Enumerator
from .store import (
    STATE_CHARGING_LOCKED,
    STATE_DOWNLOADABLE,
    STATE_EXPORTED,
    STATE_FAILED,
    STATE_FETCHED,
    STATE_NO_SUBTITLE,
    STATE_PENDING,
    STATE_TRANSCRIBED,
    Store,
)
from .types import Document, Video, expand_pages

Progress = Callable[[str, int, int], None]
PROGRESS_INTERVAL = 0.2


@dataclass
class ProbeStats:
    total: int = 0
    downloadable: int = 0
    no_subtitle: int = 0
    charging_locked: int = 0
    failed: int = 0
    skipped: int = 0
    hint: str = ""
    first_error: str = ""


@dataclass
class DownloadStats:
    total: int = 0
    fetched: int = 0
    no_subtitle: int = 0
    charging_locked: int = 0
    failed: int = 0
    transcribed: int = 0
    transcribe_failed: int = 0
    first_error: str = ""


@dataclass
class TranscribeStats:
    total: int = 0
    ok: int = 0
    failed: int = 0
    first_error: str = ""


def _emit(progress: Progress | None, op: str, done: int, total: int) -> None:
    if progress is not None:
        progress(op, done, total)


class _Throttle:
    def __init__(self, total: int, emit: Callable[[int, int], None] | None):
        self.total = total
        self.emit = emit
        self.start = time.monotonic()
        self.last = 0.0

    def tick(self, done: int) -> None:
        if self.emit is None:
            return
        now = time.monotonic()
        if done < self.total and now - self.last < PROGRESS_INTERVAL:
            return
        self.last = now
        self.emit(done, self.total)


def is_session_expired(exc: BaseException) -> bool:
    return isinstance(exc, APIError) and exc.session_expired


def is_risk_control(exc: BaseException) -> bool:
    return isinstance(exc, RiskControlError)


def format_pubdate(sec: int) -> str:
    if sec <= 0:
        return "-"
    return time.strftime("%Y-%m-%d", time.localtime(sec))


def _display_width(text: str) -> int:
    import unicodedata

    return sum(
        2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1 for ch in text
    )


def _pad(text: str, width: int) -> str:
    return text + " " * max(0, width - _display_width(text))


def format_videos_table(videos: list[Video]) -> str:
    headers = ["序号", "标题", "作者", "发布日期", "状态", "语言", "充电"]
    rows = [
        [
            str(i),
            v.title,
            v.author,
            format_pubdate(v.pubdate),
            v.state,
            v.lang,
            "是" if v.is_charging else "否",
        ]
        for i, v in enumerate(videos, start=1)
    ]
    widths = [
        max(_display_width(h), *(_display_width(r[c]) for r in rows)) if rows else _display_width(h)
        for c, h in enumerate(headers)
    ]
    lines = ["  ".join(_pad(h, w) for h, w in zip(headers, widths)).rstrip()]
    for row in rows:
        lines.append("  ".join(_pad(cell, w) for cell, w in zip(row, widths)).rstrip())
    return "\n".join(lines)


class Runtime:
    def __init__(self, cfg: Config, out: TextIO | None = None):
        self.cfg = cfg
        self.out = out or sys.stdout
        self._store: Store | None = None
        self._limiter: RateLimiter | None = None
        self._client: Client | None = None
        self._lock = threading.Lock()
        self.op_lock = threading.Lock()
        self.last_probe_stats: ProbeStats | None = None
        self.last_download_stats: DownloadStats | None = None
        self.last_transcribe_stats: TranscribeStats | None = None

    # -- resources ------------------------------------------------------

    def store(self) -> Store:
        if self._store is None:
            self._store = Store(self.cfg.db_path)
        return self._store

    def close(self) -> None:
        if self._store is not None:
            self._store.close()
            self._store = None
        if self._client is not None:
            self._client.close()
            self._client = None

    def limiter(self) -> RateLimiter:
        if self._limiter is None:
            self._limiter = RateLimiter(self.cfg.qps)
        return self._limiter

    def client(self) -> Client:
        if self._client is None:
            self._client = Client("", self.limiter())
        sessdata, cookie = load_session(self.cfg.session_path)
        if sessdata:
            self._client.sessdata = sessdata
        else:
            self._client.sessdata = self.cfg.sessdata
        self._client.cookie = cookie
        return self._client

    def enumerator(self) -> Enumerator:
        return Enumerator(self.client(), self.cfg.concurrency)

    def resolve_input_url(self, raw: str) -> str:
        if not urlparse.is_short_link(raw):
            return raw
        final = self.client().resolve_url(raw.strip())
        if urlparse.is_short_link(final):
            raise ValueError(f"b23.tv 短链没有跳转到 B站地址：{final!r}")
        return final

    # -- probe ----------------------------------------------------------

    def probe(
        self,
        raw_url: str,
        kind_spec: str = "",
        force: bool = False,
        limit: int = DEFAULT_SEARCH_LIMIT,
        progress: Progress | None = None,
    ) -> ProbeStats:
        stats = ProbeStats()
        self.last_probe_stats = stats
        raw_url = self.resolve_input_url(raw_url)
        if kind_spec:
            kind = urlparse.parse_source_kind(kind_spec)
            if kind is None:
                raise ValueError(f"probe: 未知来源类型 {kind_spec!r}")
            key = urlparse.parse_url_for_kind(raw_url, kind)
        else:
            kind, key = urlparse.parse_url(raw_url)

        try:
            infos = self._collect_videos(kind, key, limit)
        except BaseException as exc:  # noqa: BLE001 - whole-leg failures get a hint
            if is_session_expired(exc):
                stats.first_error = (
                    "登录态失效（" + str(exc) + "）。请先运行 `bili-sub-py login`。"
                )
                print(stats.first_error, file=self.out)
            elif is_risk_control(exc):
                stats.first_error = (
                    "请求被 B站风控限流，已停止列出（"
                    + str(exc)
                    + "）；等几分钟再重试。"
                )
                print(stats.first_error, file=self.out)
            raise
        if kind == urlparse.KIND_SINGLE:
            stats.hint = self._season_hint(infos)
            if stats.hint:
                print(stats.hint, file=self.out)

        store = self.store()
        videos: list[Video] = []
        for info in infos:
            videos.extend(expand_pages(info, key))
        for video in videos:
            video.id = store.upsert_video(video)
        if not videos:
            print("该来源没有视频。", file=self.out)
            return stats

        stats.total = len(videos)
        if force:
            for video in store.list_videos_filtered("", key):
                if video.state in (STATE_FETCHED, STATE_TRANSCRIBED, STATE_EXPORTED):
                    store.set_state(video.id, STATE_PENDING, "")

        _emit(progress, "probe", 0, stats.total)
        _emit(progress, "probe", stats.total, stats.total)
        print(f"已列出 {stats.total} 条（来源 {key}）。", file=self.out)
        print(
            "下一步：`bili-sub-py download` 会检查每个视频有没有字幕并下载；"
            "没字幕的标为「无字幕」，之后可用 `bili-sub-py transcribe` 转写。",
            file=self.out,
        )
        return stats

    def _collect_videos(self, kind: str, key: str, limit: int) -> list:
        en = self.enumerator()
        if kind == urlparse.KIND_SINGLE:
            return [en.resolve_video(key)]
        if kind == urlparse.KIND_SPACE:
            return en.enumerate_space(key)
        if kind == urlparse.KIND_FAV:
            return en.enumerate_fav(key)
        if kind == urlparse.KIND_SEASON:
            pair = urlparse.split_season_key(key)
            if pair is None:
                raise ValueError(f"probe: 合集来源键格式不对：{key!r}")
            return en.enumerate_season(*pair)
        if kind == urlparse.KIND_SEARCH:
            keyword = urlparse.split_search_key(key)
            if keyword is None:
                raise ValueError(f"probe: 搜索来源键格式不对：{key!r}")
            return en.enumerate_search(keyword, limit)
        raise ValueError(f"probe: 不支持的来源类型 {kind}")

    @staticmethod
    def _season_hint(infos: list) -> str:
        for info in infos:
            season = info.season
            if season is None or season.id == 0 or season.mid == 0:
                continue
            return (
                f"提示：这条视频属于合集《{season.title}》，共 {season.video_count()} 条。"
                f"要抓整个合集，用这个链接：\n  "
                + urlparse.season_url(str(season.mid), str(season.id))
            )
        return ""

    # -- download -------------------------------------------------------

    def download(
        self,
        lang: str,
        formats: list[str],
        transcribe: bool,
        asr_lang: str,
        model: str,
        source: str,
        progress: Progress | None = None,
    ) -> DownloadStats:
        source = urlparse.normalize_source_key(source)
        stats = self.download_subtitles(lang, formats, source, progress)
        if transcribe:
            try:
                ts = self.transcribe(asr_lang, model, source, progress)
            except Exception as exc:  # noqa: BLE001 - keep the download counts
                ts = self.last_transcribe_stats
                stats.transcribed = ts.ok if ts else 0
                stats.transcribe_failed = ts.failed if ts else 0
                stats.first_error = stats.first_error or str(exc)
                raise
            stats.transcribed = ts.ok
            stats.transcribe_failed = ts.failed
            stats.first_error = stats.first_error or ts.first_error
        return stats

    def download_subtitles(
        self,
        lang: str = "",
        formats: list[str] | None = None,
        source: str = "",
        progress: Progress | None = None,
    ) -> DownloadStats:
        formats = formats or ["srt"]
        source = urlparse.normalize_source_key(source)
        stats = DownloadStats()
        self.last_download_stats = stats
        store = self.store()
        videos = self._list_downloadable(store, source)
        if not videos:
            print("没有待处理的视频；先运行 `bili-sub-py probe`。", file=self.out)
            return stats

        stats.total = len(videos)
        _emit(progress, "download", 0, stats.total)
        client = self.client()
        en = self.enumerator()
        downloader = Downloader(client, store, en, self.cfg.subtitle_dir)
        stop = threading.Event()
        fatal: list[BaseException] = []
        fatal_lock = threading.Lock()
        done = 0
        done_lock = threading.Lock()
        throttle = _Throttle(
            stats.total,
            (lambda d, t: _emit(progress, "download", d, t)) if progress else None,
        )

        def fatal_hit(exc: BaseException) -> bool:
            if is_session_expired(exc) or is_risk_control(exc):
                with fatal_lock:
                    if not fatal:
                        fatal.append(exc)
                stop.set()
                return True
            return False

        def work(index: int, video: Video):
            nonlocal done
            if stop.is_set():
                return None
            try:
                doc = downloader.fetch_subtitle(video, lang)
                if formats:
                    self._write_formats(video, doc, formats)
                result: tuple = ("ok", video, doc, None)
            except NoSubtitle as exc:
                if not stop.is_set():
                    store.set_state(video.id, STATE_NO_SUBTITLE, "")
                result = ("no_subtitle", video, None, exc)
            except ChargingLocked as exc:
                if not stop.is_set():
                    store.set_state(video.id, STATE_CHARGING_LOCKED, "")
                result = ("charging", video, None, exc)
            except NoTrackForLang as exc:
                with fatal_lock:
                    if not fatal:
                        fatal.append(exc)
                stop.set()
                result = ("fatal", video, None, exc)
            except BaseException as exc:  # noqa: BLE001 - classified below
                if fatal_hit(exc):
                    return ("fatal", video, None, exc)
                if not stop.is_set():
                    store.set_state(video.id, STATE_FAILED, video.lang)
                result = ("failed", video, None, exc)
            with done_lock:
                done += 1
                throttle.tick(done)
            return result

        with ThreadPoolExecutor(
            max_workers=max(1, self.cfg.concurrency), thread_name_prefix="download"
        ) as pool:
            futures = [pool.submit(work, i, v) for i, v in enumerate(videos)]
            for i, future in enumerate(futures):
                result = future.result()
                if result is None:
                    continue
                kind, video, doc, exc = result
                if kind == "ok":
                    stats.fetched += 1
                    print(
                        f"第 {i + 1} 项 {video.bvid} 已下载（{doc.lang}）",
                        file=self.out,
                    )
                elif kind == "no_subtitle":
                    stats.no_subtitle += 1
                elif kind == "charging":
                    stats.charging_locked += 1
                elif kind == "failed":
                    stats.failed += 1
                    if stats.first_error == "":
                        stats.first_error = str(exc)
                    print(f"第 {i + 1} 项 {video.bvid} 失败：{exc}", file=self.out)

        if fatal:
            err = fatal[0]
            if is_risk_control(err):
                msg = (
                    "请求被 B站风控限流，已停止下载（"
                    + str(err)
                    + "）；剩余视频保持原状态，等几分钟再重试。"
                )
            elif isinstance(err, NoTrackForLang):
                msg = (
                    "指定的字幕语言在这些视频里没有匹配的轨道，已停止下载（"
                    + str(err)
                    + "）；剩余视频保持原状态。"
                )
            else:
                msg = (
                    "登录态失效，已停止下载（"
                    + str(err)
                    + "）。请先运行 `bili-sub-py login`；剩余视频保持原状态。"
                )
            print(msg, file=self.out)
            stats.first_error = stats.first_error or msg
            raise err

        _emit(progress, "download", stats.total, stats.total)
        print(
            f"下载完成：共 {stats.total}，成功 {stats.fetched}，无字幕 {stats.no_subtitle}，"
            f"充电专属 {stats.charging_locked}，失败 {stats.failed}，输出目录 {self.cfg.subtitle_dir}",
            file=self.out,
        )
        return stats

    @staticmethod
    def _list_downloadable(store: Store, source: str) -> list[Video]:
        seen: set[int] = set()
        videos: list[Video] = []
        for state in (STATE_PENDING, STATE_DOWNLOADABLE):
            for video in store.list_videos_filtered(state, source):
                if video.id not in seen:
                    seen.add(video.id)
                    videos.append(video)
        return videos

    def _write_formats(self, video: Video, doc: Document, formats: list[str]) -> None:
        self.cfg.subtitle_dir.mkdir(parents=True, exist_ok=True)
        stem = page_stem(video)
        for fmt in formats:
            if fmt == export_mod.FORMAT_SRT:
                continue
            (self.cfg.subtitle_dir / f"{stem}.{fmt}").write_text(
                export_mod.render(doc, fmt), encoding="utf-8", newline="\n"
            )

    # -- transcribe -----------------------------------------------------

    def transcribe(
        self,
        asr_lang: str = "zh",
        model: str = "",
        source: str = "",
        progress: Progress | None = None,
    ) -> TranscribeStats:
        stats = TranscribeStats()
        self.last_transcribe_stats = stats
        source = urlparse.normalize_source_key(source)
        store = self.store()
        videos = store.list_videos_filtered(STATE_NO_SUBTITLE, source)
        if not videos:
            print("没有需要转写的无字幕视频。", file=self.out)
            return stats

        stats.total = len(videos)
        model = model or self.cfg.asr_model
        backend = Transcriber(model, self.cfg.asr_simplify)
        client = self.client()
        en = self.enumerator()
        fetcher = AudioFetcher(client, en, self.cfg.audio_dir)
        self.cfg.audio_dir.mkdir(parents=True, exist_ok=True)
        self._sweep_stale_audio()

        throttle = _Throttle(
            stats.total,
            (lambda d, t: _emit(progress, "transcribe", d, t)) if progress else None,
        )
        _emit(progress, "transcribe", 0, stats.total)
        audio_queue: queue.Queue = queue.Queue(maxsize=2)
        stop = threading.Event()

        def fetch_ahead() -> None:
            for index, video in enumerate(videos):
                if stop.is_set():
                    break
                try:
                    path = fetcher.fetch(video)
                    item = (index, video, path, None)
                except BaseException as exc:  # noqa: BLE001 - classified below
                    item = (index, video, None, exc)
                    if is_session_expired(exc) or is_risk_control(exc):
                        audio_queue.put(item)
                        return
                audio_queue.put(item)
            audio_queue.put(None)

        producer = threading.Thread(target=fetch_ahead, name="audio-prefetch", daemon=True)
        producer.start()
        try:
            while True:
                item = audio_queue.get()
                if item is None:
                    break
                index, video, audio_path, error = item
                if error is not None:
                    if is_session_expired(error):
                        print(
                            f"登录态失效，已停止转写（{error}）。请先运行 `bili-sub-py login`；"
                            f"剩余 {stats.total - index - 1} 个视频保持原状态。",
                            file=self.out,
                        )
                        stop.set()
                        raise error
                    if is_risk_control(error):
                        print(
                            f"请求被 B站风控限流，已停止转写（{error}）；"
                            f"剩余 {stats.total - index - 1} 个视频保持原状态，等几分钟再重试。",
                            file=self.out,
                        )
                        stop.set()
                        raise error
                    stats.failed += 1
                    print(f"第 {index + 1} 项 {video.bvid} 转写失败：{error}", file=self.out)
                    store.set_state(video.id, STATE_FAILED, video.lang)
                    throttle.tick(index + 1)
                    continue

                try:
                    segments = backend.transcribe(
                        audio_path, asr_lang, model
                    )
                    segments = clean_segments(
                        segments, asr_lang, self.cfg.asr_simplify
                    )
                    if not segments:
                        raise RuntimeError("没有识别到语音内容")
                    doc = Document(
                        bvid=video.bvid,
                        cid=video.cid,
                        title=video.title,
                        author=video.author,
                        lang=asr_lang,
                        segments=segments,
                    )
                    self.cfg.subtitle_dir.mkdir(parents=True, exist_ok=True)
                    (self.cfg.subtitle_dir / (page_stem(video) + ".srt")).write_text(
                        export_mod.render(doc, export_mod.FORMAT_SRT),
                        encoding="utf-8",
                        newline="\n",
                    )
                    ir_json = json.dumps(doc.to_json(), ensure_ascii=False)
                    store.save_transcript(video.id, "faster-whisper", asr_lang, ir_json)
                    store.set_state(video.id, STATE_TRANSCRIBED, asr_lang)
                    stats.ok += 1
                    print(
                        f"第 {index + 1} 项 {video.bvid} 已转写（{asr_lang}）",
                        file=self.out,
                    )
                except (EngineError, KeyboardInterrupt) as exc:
                    stop.set()
                    print(
                        f"ASR 引擎不可用，已停止转写（{exc}）；"
                        f"剩余 {stats.total - index - 1} 个视频保持原状态。",
                        file=self.out,
                    )
                    raise
                except BaseException as exc:  # noqa: BLE001 - per-video failure
                    stats.failed += 1
                    print(f"第 {index + 1} 项 {video.bvid} 转写失败：{exc}", file=self.out)
                    store.set_state(video.id, STATE_FAILED, video.lang)
                finally:
                    Path(audio_path).unlink(missing_ok=True)
                throttle.tick(index + 1)
        finally:
            stop.set()
            producer.join(timeout=1)
            while True:
                try:
                    leftover = audio_queue.get_nowait()
                except queue.Empty:
                    break
                if leftover and leftover[2]:
                    Path(leftover[2]).unlink(missing_ok=True)

        print(
            f"转写完成：共 {stats.total}，成功 {stats.ok}，失败 {stats.failed}",
            file=self.out,
        )
        return stats

    def _sweep_stale_audio(self) -> None:
        cutoff = time.time() - 30 * 60
        try:
            for entry in self.cfg.audio_dir.iterdir():
                if (
                    entry.is_file()
                    and (entry.suffix == ".m4s" or entry.name.endswith(".m4s.part"))
                    and entry.stat().st_mtime < cutoff
                ):
                    entry.unlink(missing_ok=True)
        except OSError:
            pass

    # -- export ---------------------------------------------------------

    def export(
        self,
        formats: list[str],
        include_exported: bool = False,
        progress: Progress | None = None,
    ) -> str:
        store = self.store()
        states = [STATE_FETCHED, STATE_TRANSCRIBED]
        if include_exported:
            states.append(STATE_EXPORTED)
        seen: set[int] = set()
        videos: list[Video] = []
        for state in states:
            for video in store.list_videos_filtered(state, ""):
                if video.id not in seen:
                    seen.add(video.id)
                    videos.append(video)
        if not videos:
            _emit(progress, "export", 0, 0)
            print(
                "没有尚未导出的字幕（如需重新导出，请勾选「包含已导出的」或用 --include-exported）。",
                file=self.out,
            )
            return ""

        _emit(progress, "export", 0, len(videos))
        throttle = _Throttle(
            len(videos),
            (lambda d, t: _emit(progress, "export", d, t)) if progress else None,
        )
        docs: dict[str, Document] = {}
        stems: dict[str, str] = {}
        key_ids: dict[str, int] = {}
        done = 0
        for video in videos:
            record = store.get_transcript(video.id)
            if record is None:
                print(f"export: 跳过 {video.bvid}：没有字幕记录", file=self.out)
                done += 1
                throttle.tick(done)
                continue
            try:
                doc = Document.from_json(json.loads(record[2]))
            except (TypeError, ValueError) as exc:
                print(f"export: 跳过 {video.bvid}：IR 解析失败: {exc}", file=self.out)
                done += 1
                throttle.tick(done)
                continue
            if not doc.author:
                doc.author = video.author
            key = video.bvid + (f"_P{video.page_index}" if video.page_index > 1 else "")
            docs[key] = doc
            stems[key] = page_stem(video)
            key_ids[key] = video.id
            done += 1
            throttle.tick(done)
        if not docs:
            raise RuntimeError("export: 没有可导出的字幕，所有视频都缺少有效 IR")

        zip_path = export_mod.pack(self.cfg.export_dir, docs, stems, formats)
        store.mark_exported(list(key_ids.values()))
        _emit(progress, "export", len(videos), len(videos))
        print(
            f"导出完成：{zip_path}（{len(docs)} 个视频 × {len(formats)} 种格式）",
            file=self.out,
        )
        return zip_path

    # -- run ------------------------------------------------------------

    def run(
        self,
        raw_url: str,
        kind_spec: str = "",
        lang: str = "",
        asr_lang: str = "zh",
        model: str = "",
        transcribe: bool = True,
        formats: list[str] | None = None,
        force: bool = False,
        limit: int = DEFAULT_SEARCH_LIMIT,
        progress: Progress | None = None,
    ) -> None:
        formats = formats or [export_mod.FORMAT_TEXT]
        raw_url = self.resolve_input_url(raw_url)
        if kind_spec:
            kind = urlparse.parse_source_kind(kind_spec)
            if kind is None:
                raise ValueError(f"未知的来源类型: {kind_spec}")
            source = urlparse.parse_url_for_kind(raw_url, kind)
        else:
            _, source = urlparse.parse_url(raw_url)
        source = urlparse.normalize_source_key(source)

        self.probe(raw_url, kind_spec, force, limit, progress)
        self.download_subtitles(lang, formats, source, progress)
        asr_error: BaseException | None = None
        if transcribe:
            try:
                self.transcribe(asr_lang, model, source, progress)
            except KeyboardInterrupt:
                raise
            except Exception as exc:  # noqa: BLE001 - export still runs
                asr_error = exc
        self.export(formats, False, progress)
        if asr_error is not None:
            print(
                f"\n注意：字幕已下载并导出，但转写阶段失败：{asr_error}",
                file=self.out,
            )
            raise asr_error

    # -- status / maintenance -------------------------------------------

    def status(self, state: str = "") -> int:
        videos = self.store().list_videos(state)
        if not videos:
            print("没有匹配的视频记录。", file=self.out)
            return 0
        print(format_videos_table(videos), file=self.out)
        print(f"共 {len(videos)} 个视频", file=self.out)
        return len(videos)

    def retry(self) -> int:
        videos = self.store().list_videos(STATE_FAILED)
        for video in videos:
            self.store().set_state(video.id, STATE_PENDING, video.lang)
        print(
            f"已把 {len(videos)} 个失败视频重置为 pending，"
            "请重新运行 probe <url> 再重试。",
            file=self.out,
        )
        return len(videos)

    def reset(self) -> None:
        paths.check_reset_path(self.cfg.data_dir_raw)
        if self.cfg.data_dir.exists():
            shutil.rmtree(self.cfg.data_dir)
        print(f"已清空数据目录 {self.cfg.data_dir}", file=self.out)

    # -- login ----------------------------------------------------------

    def save_login(self, sessdata: str = "", cookie: str = "") -> None:
        check = Client(self.cfg.sessdata, RateLimiter(self.cfg.qps))
        try:
            check.sessdata = sessdata or extract_sessdata(cookie)
            check.cookie = cookie
            ok, _uname = check.nav_status()
            if not ok:
                raise RuntimeError("login: 校验未通过（未登录），已保留原有登录信息")
        finally:
            check.close()
        save_session(self.cfg.session_path, sessdata, cookie)

    def login_status(self) -> tuple[bool, str]:
        client = self.client()
        return client.nav_status()


def build_runtime(env: str, out: TextIO | None = None) -> Runtime:
    return Runtime(load_config(env), out)
