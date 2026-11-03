"""FastAPI web server: the same JSON API and SSE stream the Go server exposes."""

from __future__ import annotations

import asyncio
import json
import queue
import threading
from pathlib import Path
from urllib.parse import urlparse

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, Response, StreamingResponse
from starlette.concurrency import run_in_threadpool
from starlette.staticfiles import StaticFiles

from ..bili import QRLoginClient
from ..bili.login import save_session
from ..pipeline import urlparse as up
from ..pipeline.enumerate import DEFAULT_SEARCH_LIMIT
from ..runtime import Runtime
from .hub import Hub

MAX_BODY = 1 << 20
WEB_DIR = Path(__file__).with_name("web")


def _allowed_origin(origin: str) -> bool:
    if not origin:
        return False
    try:
        parsed = urlparse(origin)
    except ValueError:
        return False
    if parsed.scheme in ("moz-extension", "chrome-extension"):
        return bool(parsed.netloc)
    host = parsed.hostname or ""
    return (
        host in ("localhost", "127.0.0.1", "::1")
        or host == "bilibili.com"
        or host.endswith(".bilibili.com")
    )


def _error(message: str, status: int = 400) -> JSONResponse:
    return JSONResponse({"error": message}, status_code=status)


async def _body(request: Request) -> dict:
    raw = await request.body()
    if len(raw) > MAX_BODY:
        raise ValueError("请求体过大")
    if not raw.strip():
        return {}
    data = json.loads(raw)
    if not isinstance(data, dict):
        raise ValueError("请求体必须是 JSON 对象")
    return data


def _parse_int(raw: str | None, default: int) -> int:
    if raw is None or raw == "":
        return default
    try:
        return int(raw)
    except ValueError:
        return default


def create_app(rt: Runtime) -> FastAPI:
    app = FastAPI(docs_url=None, redoc_url=None, openapi_url=None)
    hub = Hub()

    @app.middleware("http")
    async def cors_and_content_type(request: Request, call_next):
        origin = request.headers.get("origin", "")
        if request.method == "OPTIONS":
            response = Response(status_code=204)
        elif request.method == "POST" and not request.headers.get(
            "content-type", ""
        ).startswith("application/json"):
            return _error(
                "Content-Type 必须是 application/json（收到 "
                f'"{request.headers.get("content-type", "")}"）',
                400,
            )
        else:
            response = await call_next(request)
        if _allowed_origin(origin):
            response.headers["Access-Control-Allow-Origin"] = origin
            response.headers["Vary"] = "Origin"
            response.headers["Access-Control-Allow-Methods"] = "GET, POST, OPTIONS"
            response.headers["Access-Control-Allow-Headers"] = "Content-Type"
            response.headers["Access-Control-Max-Age"] = "600"
        return response

    def progress(op: str, done: int, total: int) -> None:
        hub.broadcast({"type": "progress", "data": {"op": op, "done": done, "total": total}})

    def acquire() -> bool:
        return rt.op_lock.acquire(blocking=False)

    # -- videos / stats -------------------------------------------------

    @app.get("/api/videos")
    async def videos(request: Request):
        q = request.query_params
        state = (q.get("state") or "").strip()
        source = (q.get("source") or "").strip()
        offset = max(0, _parse_int(q.get("offset"), 0))
        limit = _parse_int(q.get("limit"), 100)
        limit = min(500, max(1, limit))
        store = rt.store()
        total = store.count_videos(state, source)
        rows = store.list_videos_filtered(state, source)
        page = rows[offset : offset + limit] if offset < len(rows) else []
        return {
            "items": [v.to_api() for v in page],
            "total": total,
            "offset": offset,
            "limit": limit,
        }

    @app.get("/api/stats")
    async def stats():
        rows = rt.store().list_videos("")
        out: dict[str, int] = {}
        for video in rows:
            out[video.state] = out.get(video.state, 0) + 1
        return out

    # -- probe ----------------------------------------------------------

    @app.post("/api/probe")
    async def probe(request: Request):
        try:
            body = await _body(request)
        except (ValueError, json.JSONDecodeError) as exc:
            return _error(f"请求体不是合法 JSON: {exc}")
        raw = str(body.get("url") or "").strip()
        if not raw:
            return _error("url 不能为空")
        kind = str(body.get("kind") or "")
        force = bool(body.get("force"))
        try:
            resolved = await run_in_threadpool(rt.resolve_input_url, raw)
        except Exception as exc:  # noqa: BLE001
            return _error(f"无法解析短链: {exc}", 502)
        try:
            if kind:
                parsed_kind = up.parse_source_kind(kind)
                if parsed_kind is None:
                    return _error(f"未知的来源类型: {kind}")
                source = up.parse_url_for_kind(resolved, parsed_kind)
            else:
                parsed_kind, source = up.parse_url(resolved)
        except ValueError as exc:
            return _error(f"无法解析链接: {exc}")

        if not acquire():
            return _error("已有任务在执行，请等它完成后再试", 409)
        try:
            try:
                await run_in_threadpool(
                    rt.probe, resolved, kind, force, DEFAULT_SEARCH_LIMIT, progress
                )
            except Exception as exc:  # noqa: BLE001 - partial results are valid
                stats_obj = rt.last_probe_stats
                if stats_obj is None or (
                    stats_obj.total == 0
                    and stats_obj.downloadable == 0
                    and stats_obj.no_subtitle == 0
                    and stats_obj.charging_locked == 0
                    and stats_obj.failed == 0
                    and stats_obj.skipped == 0
                ):
                    return _error(str(exc), 500)
                summary = {
                    "probed": stats_obj.total,
                    "downloadable": stats_obj.downloadable,
                    "no_subtitle": stats_obj.no_subtitle,
                    "charging_locked": stats_obj.charging_locked,
                    "failed": stats_obj.failed,
                    "skipped": stats_obj.skipped,
                    "hint": stats_obj.hint,
                    "reason": stats_obj.first_error or str(exc),
                    "source": source,
                    "kind": parsed_kind,
                }
                hub.broadcast({"type": "probe", "data": summary})
                return summary
        finally:
            rt.op_lock.release()

        stats_obj = rt.last_probe_stats
        summary = {
            "probed": stats_obj.total if stats_obj else 0,
            "downloadable": stats_obj.downloadable if stats_obj else 0,
            "no_subtitle": stats_obj.no_subtitle if stats_obj else 0,
            "charging_locked": stats_obj.charging_locked if stats_obj else 0,
            "failed": stats_obj.failed if stats_obj else 0,
            "skipped": stats_obj.skipped if stats_obj else 0,
            "hint": stats_obj.hint if stats_obj else "",
            "reason": stats_obj.first_error if stats_obj else "",
            "source": source,
            "kind": parsed_kind,
        }
        hub.broadcast({"type": "probe", "data": summary})
        return summary

    # -- download -------------------------------------------------------

    @app.post("/api/download")
    async def download(request: Request):
        try:
            body = await _body(request)
        except (ValueError, json.JSONDecodeError) as exc:
            return _error(f"请求体不是合法 JSON: {exc}")
        if not acquire():
            return _error("已有任务在执行，请等它完成后再试", 409)
        try:
            stats_obj = None
            error_text = ""
            try:
                stats_obj = await run_in_threadpool(
                    rt.download_subtitles,
                    str(body.get("lang") or ""),
                    ["srt"],
                    str(body.get("source") or ""),
                    progress,
                )
            except Exception as exc:  # noqa: BLE001
                stats_obj = rt.last_download_stats
                error_text = str(exc)
            summary = {
                "fetched": stats_obj.fetched if stats_obj else 0,
                "no_subtitle": stats_obj.no_subtitle if stats_obj else 0,
                "charging_locked": stats_obj.charging_locked if stats_obj else 0,
                "failed": stats_obj.failed if stats_obj else 0,
                "transcribed": 0,
                "transcribe_failed": 0,
                "error": (stats_obj.first_error if stats_obj else "") or error_text,
            }
            if body.get("transcribe") and not error_text:
                try:
                    ts = await run_in_threadpool(
                        rt.transcribe,
                        str(body.get("asr_lang") or "zh"),
                        str(body.get("model") or ""),
                        str(body.get("source") or ""),
                        progress,
                    )
                    summary["transcribed"] = ts.ok
                    summary["transcribe_failed"] = ts.failed
                except Exception as exc:  # noqa: BLE001
                    ts = rt.last_transcribe_stats
                    summary["transcribed"] = ts.ok if ts else 0
                    summary["transcribe_failed"] = ts.failed if ts else 0
                    summary["error"] = summary["error"] or str(exc)
            if error_text and not (
                summary["fetched"]
                or summary["no_subtitle"]
                or summary["charging_locked"]
                or summary["transcribed"]
                or summary["failed"]
                or summary["transcribe_failed"]
            ):
                return _error(error_text, 500)
            hub.broadcast({"type": "download", "data": summary})
            return summary
        finally:
            rt.op_lock.release()

    # -- transcribe -----------------------------------------------------

    @app.post("/api/transcribe")
    async def transcribe(request: Request):
        try:
            body = await _body(request)
        except (ValueError, json.JSONDecodeError) as exc:
            return _error(f"请求体不是合法 JSON: {exc}")
        if not acquire():
            return _error("已有任务在执行，请等它完成后再试", 409)
        try:
            error_text = ""
            try:
                ts = await run_in_threadpool(
                    rt.transcribe,
                    str(body.get("asr_lang") or "zh"),
                    str(body.get("model") or ""),
                    str(body.get("source") or ""),
                    progress,
                )
            except Exception as exc:  # noqa: BLE001
                ts = rt.last_transcribe_stats
                error_text = str(exc)
            summary = {
                "total": ts.total if ts else 0,
                "transcribed": ts.ok if ts else 0,
                "failed": ts.failed if ts else 0,
                "error": error_text,
            }
            if error_text and (ts is None or (ts.ok == 0 and ts.failed == 0)):
                return _error(error_text, 500)
            hub.broadcast({"type": "transcribe", "data": summary})
            return summary
        finally:
            rt.op_lock.release()

    # -- export ---------------------------------------------------------

    @app.post("/api/export")
    async def export(request: Request):
        try:
            body = await _body(request)
        except (ValueError, json.JSONDecodeError) as exc:
            return _error(f"请求体不是合法 JSON: {exc}")
        formats = [str(f) for f in (body.get("formats") or [])]
        if not formats:
            return _error("formats 不能为空")
        invalid = [f for f in formats if f not in ("srt", "txt", "md", "json")]
        if invalid:
            return _error(f"不支持的格式: {invalid[0]}")
        if not acquire():
            return _error("已有任务在执行，请等它完成后再试", 409)
        try:
            try:
                zip_path = await run_in_threadpool(
                    rt.export, formats, bool(body.get("include_exported")), progress
                )
            except Exception as exc:  # noqa: BLE001
                return _error(str(exc), 500)
            if not zip_path:
                return {
                    "zip": "",
                    "message": "没有尚未导出的字幕（可勾选「包含已导出的」后重试）",
                }
            hub.broadcast({"type": "export", "data": {"zip": zip_path}})
            return {"zip": zip_path}
        finally:
            rt.op_lock.release()

    # -- reset ----------------------------------------------------------

    @app.post("/api/reset")
    async def reset(request: Request):
        try:
            body = await _body(request)
        except (ValueError, json.JSONDecodeError) as exc:
            return _error(f"请求体不是合法 JSON: {exc}")
        if not body.get("confirm"):
            return _error("reset 需要 confirm=true 确认")
        try:
            deleted = await run_in_threadpool(rt.store().delete_all_videos)
        except Exception as exc:  # noqa: BLE001
            return _error(str(exc), 500)
        hub.broadcast({"type": "reset", "data": {"deleted": deleted}})
        return {"ok": True, "deleted": deleted}

    # -- login ----------------------------------------------------------

    @app.post("/api/login/sessdata")
    async def login_sessdata(request: Request):
        try:
            body = await _body(request)
        except (ValueError, json.JSONDecodeError) as exc:
            return _error(f"请求体不是合法 JSON: {exc}")
        sessdata = str(body.get("sessdata") or "")
        cookie = str(body.get("cookie") or "")
        if not sessdata and not cookie:
            return _error("sessdata 与 cookie 不能同时为空")
        try:
            await run_in_threadpool(rt.save_login, sessdata, cookie)
        except Exception as exc:  # noqa: BLE001
            return _error(str(exc), 500)
        return {"ok": True}

    @app.post("/api/handoff")
    async def handoff(request: Request):
        try:
            body = await _body(request)
        except (ValueError, json.JSONDecodeError) as exc:
            return _error(f"请求体不是合法 JSON: {exc}")
        cookie = str(body.get("cookie") or "")
        url = str(body.get("url") or "")
        if not cookie and not url:
            return _error("cookie 与 url 不能同时为空")
        response = {"ok": True, "logged_in": False, "url": url, "error": ""}
        if cookie:
            try:
                await run_in_threadpool(rt.save_login, "", cookie)
                response["logged_in"] = True
            except Exception as exc:  # noqa: BLE001
                response["error"] = f"登录失败: {exc}"
        if response["logged_in"]:
            try:
                ok, uname = await run_in_threadpool(rt.login_status)
                response["logged_in"] = ok
                if uname:
                    response["uname"] = uname
            except Exception:  # noqa: BLE001
                pass
        hub.broadcast({"type": "handoff", "data": response})
        return response

    @app.get("/api/login/status")
    async def login_status():
        try:
            ok, uname = await run_in_threadpool(rt.login_status)
        except Exception:  # noqa: BLE001
            return {}
        out = {"logged_in": ok}
        if uname:
            out["uname"] = uname
        return out

    @app.get("/api/login/qrcode")
    async def login_qrcode():
        try:
            import segno
        except ImportError:
            return _error("缺少 segno，无法生成二维码：pip install segno", 500)

        url_queue: queue.Queue = queue.Queue(maxsize=1)
        qr_client = QRLoginClient()

        def worker() -> None:
            try:
                sessdata = qr_client.login(lambda u: url_queue.put(u))
                save_session(rt.cfg.session_path, sessdata, "")
                hub.broadcast({"type": "login", "data": {"ok": True}})
            except Exception as exc:  # noqa: BLE001
                if url_queue.empty():
                    url_queue.put(exc)
                else:
                    hub.broadcast({"type": "login", "data": {"error": str(exc)}})
            finally:
                qr_client.close()

        threading.Thread(target=worker, name="qr-login", daemon=True).start()
        try:
            item = await run_in_threadpool(url_queue.get, True, 15)
        except queue.Empty:
            return _error("生成二维码超时", 500)
        if isinstance(item, BaseException):
            return _error(str(item), 500)
        png = segno.make(item).png_data_uri(scale=6)
        return {"url": item, "png": png}

    # -- SSE ------------------------------------------------------------

    @app.get("/api/events")
    async def events(request: Request):
        q, cancel = hub.subscribe()

        async def stream():
            try:
                while True:
                    if await request.is_disconnected():
                        break
                    try:
                        event = await asyncio.to_thread(q.get, True, 1.0)
                    except queue.Empty:
                        yield ": ping\n\n"
                        continue
                    if event is None:
                        break
                    yield f"data: {json.dumps(event, ensure_ascii=False)}\n\n"
            finally:
                cancel()

        return StreamingResponse(
            stream(),
            media_type="text/event-stream",
            headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
        )

    app.mount("/", StaticFiles(directory=str(WEB_DIR), html=True), name="web")
    return app
