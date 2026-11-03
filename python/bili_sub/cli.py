"""Command line interface (feature parity with the Go binary)."""

from __future__ import annotations

import click

from . import export as export_mod
from .bili import QRLoginClient
from .config import load as load_config
from .pipeline.enumerate import DEFAULT_SEARCH_LIMIT
from .paths import resolve_env_path
from .runtime import Runtime


@click.group(context_settings={"help_option_names": ["-h", "--help"]})
@click.option("--env", default=".env", show_default=True, help="配置文件路径（KEY=VALUE）")
@click.pass_context
def cli(ctx: click.Context, env: str) -> None:
    """bili-sub（Python 版）：B站字幕采集与本地 ASR 流水线"""
    ctx.obj = env


def _runtime(ctx: click.Context) -> Runtime:
    return Runtime(load_config(str(resolve_env_path(ctx.obj))))


def _formats(raw: str) -> list[str]:
    try:
        return export_mod.parse_formats(raw)
    except ValueError as exc:
        raise click.BadParameter(str(exc)) from exc


def _limit(raw: int) -> int:
    if raw < 0:
        raise click.BadParameter("--limit 不能为负数（0 表示不限）")
    return raw


@cli.command()
@click.option("--sessdata", default="", help="直接保存的 SESSDATA 值")
@click.option("--cookie", default="", help="浏览器完整 Cookie")
@click.option("--qr", is_flag=True, help="扫码登录")
def login(sessdata: str, cookie: str, qr: bool) -> None:
    """保存 SESSDATA/完整 Cookie 或扫码登录"""
    chosen = sum(bool(x) for x in (sessdata, cookie, qr))
    if chosen == 0:
        raise click.UsageError("需要 --sessdata <值>、--cookie <值> 或 --qr")
    if chosen > 1:
        raise click.UsageError("--sessdata、--cookie 与 --qr 只能选一个")
    rt = _runtime(click.get_current_context())
    try:
        if qr:
            qr_client = QRLoginClient()
            try:
                value = qr_client.login(
                    lambda url: click.echo(f"请用哔哩哔哩 App 扫描二维码：{url}")
                )
            finally:
                qr_client.close()
            rt.cfg.session_path.parent.mkdir(parents=True, exist_ok=True)
            from .bili.login import save_session

            save_session(rt.cfg.session_path, value, "")
        else:
            rt.save_login(sessdata, cookie)
        click.echo("登录信息已保存。")
    finally:
        rt.close()


@cli.command()
@click.argument("url")
@click.option("--kind", default="", help="来源类型（single/space/fav/season/search）")
@click.option("--force", is_flag=True, help="重新列出并重置已完成的行")
@click.option("--limit", type=int, default=DEFAULT_SEARCH_LIMIT, show_default=True)
def probe(url: str, kind: str, force: bool, limit: int) -> None:
    """枚举来源并探测字幕可用性（不下载）"""
    rt = _runtime(click.get_current_context())
    try:
        rt.probe(url, kind, force, _limit(limit))
    finally:
        rt.close()


@cli.command()
@click.option("--lang", default="", help="字幕语言（如 zh、en），空则自动选择")
@click.option("--format", "format_", default="srt", show_default=True)
@click.option("--transcribe", "do_transcribe", is_flag=True, help="对无字幕视频启用本地 ASR")
@click.option("--source", default="", help="只处理该来源（source_key）")
@click.option("--asr-lang", default="zh", show_default=True)
@click.option("--model", default="", help="Whisper 模型名")
def download(
    lang: str,
    format_: str,
    do_transcribe: bool,
    source: str,
    asr_lang: str,
    model: str,
) -> None:
    """下载可下载字幕（--transcribe 接入本地 ASR）"""
    rt = _runtime(click.get_current_context())
    try:
        rt.download(lang, _formats(format_), do_transcribe, asr_lang, model, source)
    finally:
        rt.close()


@cli.command()
@click.option("--lang", "asr_lang", default="zh", show_default=True)
@click.option("--model", default="", help="Whisper 模型名")
@click.option("--source", default="", help="只转写该来源")
def transcribe(asr_lang: str, model: str, source: str) -> None:
    """对无字幕视频做本地 ASR 转写"""
    rt = _runtime(click.get_current_context())
    try:
        rt.transcribe(asr_lang, model, source)
    finally:
        rt.close()


@cli.command()
@click.option("--format", "format_", default="txt", show_default=True)
@click.option("--include-exported", is_flag=True, help="连同已导出的字幕一起打包")
def export(format_: str, include_exported: bool) -> None:
    """把尚未导出的字幕打包成 zip"""
    rt = _runtime(click.get_current_context())
    try:
        rt.export(_formats(format_), include_exported)
    finally:
        rt.close()


@cli.command()
@click.option("--state", default="", help="只显示该状态的视频")
def status(state: str) -> None:
    """打印视频状态表"""
    rt = _runtime(click.get_current_context())
    try:
        rt.status(state)
    finally:
        rt.close()


@cli.command()
def retry() -> None:
    """把 failed 视频重置为 pending"""
    rt = _runtime(click.get_current_context())
    try:
        rt.retry()
    finally:
        rt.close()


@cli.command()
@click.option("--yes", is_flag=True, help="确认删除数据目录内容")
def reset(yes: bool) -> None:
    """清空本地数据目录（需要 --yes）"""
    if not yes:
        raise click.UsageError("reset: 会删除整个数据目录，需要 --yes 确认")
    rt = _runtime(click.get_current_context())
    try:
        rt.reset()
    finally:
        rt.close()


@cli.command()
@click.argument("url")
@click.option("--kind", default="", help="来源类型")
@click.option("--lang", default="", help="字幕语言")
@click.option("--asr-lang", default="zh", show_default=True)
@click.option("--model", default="", help="Whisper 模型名")
@click.option("--transcribe/--no-transcribe", "do_transcribe", default=True)
@click.option("--format", "format_", default="txt", show_default=True)
@click.option("--force", is_flag=True)
@click.option("--limit", type=int, default=DEFAULT_SEARCH_LIMIT, show_default=True)
def run(
    url: str,
    kind: str,
    lang: str,
    asr_lang: str,
    model: str,
    do_transcribe: bool,
    format_: str,
    force: bool,
    limit: int,
) -> None:
    """一条命令跑完：枚举 → 探测 → 下载 → 转写 → 导出"""
    rt = _runtime(click.get_current_context())
    try:
        rt.run(
            url,
            kind,
            lang,
            asr_lang,
            model,
            do_transcribe,
            _formats(format_),
            force,
            _limit(limit),
        )
    finally:
        rt.close()


@cli.command()
@click.option("--addr", default="127.0.0.1:8080", show_default=True)
def serve(addr: str) -> None:
    """启动 Web 界面（HTTP API + SSE + 内嵌页面）"""
    import uvicorn

    from .server.app import create_app

    rt = _runtime(click.get_current_context())
    try:
        host, _, port_text = addr.rpartition(":")
        if not host:
            host = "0.0.0.0"
        port = int(port_text)
        click.echo(f"Web 界面已启动：http://{addr if host != '0.0.0.0' else 'localhost:' + port_text}（Ctrl+C 退出）")
        uvicorn.run(create_app(rt), host=host, port=port, log_level="info")
    finally:
        rt.close()


def main() -> None:
    try:
        cli.main(standalone_mode=False)
    except click.ClickException as exc:
        exc.show()
        raise SystemExit(exc.exit_code)
    except (click.Abort, KeyboardInterrupt):
        click.echo("已取消。", err=True)
        raise SystemExit(130)
    except Exception as exc:  # noqa: BLE001 - CLI boundary
        click.echo(f"错误：{exc}", err=True)
        raise SystemExit(1)


if __name__ == "__main__":
    main()
