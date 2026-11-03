"use strict";

// The page talks to the JSON API in internal/server/http.go. /api/videos
// marshals store.Video directly, so the JSON keys are the Go field names
// (Title, Author, Pubdate, State, Lang, IsCharging, ...).

const $ = (id) => document.getElementById(id);

// PAGE_SIZE is the fixed page size the server accepts; pageOffset is the
// offset of the page currently shown and pageTotal is the server-reported total
// number of rows matching the active filter.
const PAGE_SIZE = 100;
let pageOffset = 0;
let pageTotal = 0;

const STATE_LABELS = {
  pending: "待处理",
  probed: "已获取",
  downloadable: "可下载",
  no_subtitle: "无字幕",
  charging_locked: "充电专属",
  fetched: "已抓取",
  transcribed: "已转写",
  exported: "已导出",
  failed: "失败",
};

// api performs a fetch and returns the parsed JSON body (or null for an empty
// body). A non-2xx response is turned into an Error carrying the server's
// {"error": "..."} message.
async function api(path, options) {
  const res = await fetch(path, options);
  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch (err) {
      data = null;
    }
  }
  if (!res.ok) {
    throw new Error(data && data.error ? data.error : `HTTP ${res.status}`);
  }
  return data;
}

function post(path, body) {
  return api(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

function log(message) {
  const line = `[${new Date().toLocaleTimeString()}] ${message}`;
  const box = $("log");
  box.textContent += line + "\n";
  box.scrollTop = box.scrollHeight;
}

// OP_LABELS names the long-running operations for the progress panel.
const OP_LABELS = { probe: "获取列表", download: "获取字幕", transcribe: "转写", export: "导出" };

// showProgress fills the persistent panel under the header. A tick with total 0
// (an operation that reports no count) shows 0% and a done/0 label. The panel
// is never hidden on completion, so progress stays visible.
function showProgress(op, done, total) {
  const panel = $("progress-panel");
  if (!panel) {
    return;
  }
  panel.hidden = false;
  $("progress-label").textContent = `${OP_LABELS[op] || op} ${done}/${total}`;
  $("progress-fill").style.width = total > 0 ? `${(done / total) * 100}%` : "0%";
}

// showFailure keeps the panel visible after a failed operation instead of
// hiding it, so the bar does not silently disappear.
function showFailure() {
  const panel = $("progress-panel");
  if (!panel) {
    return;
  }
  panel.hidden = false;
  $("progress-label").textContent = "失败";
  $("progress-fill").style.width = "0%";
}

// beginProgress shows the bar for a locally started operation before the
// server's first tick arrives.
function beginProgress(op) {
  showProgress(op, 0, 0);
  $("progress-label").textContent = "准备中…";
}

function formatPubdate(seconds) {
  if (!seconds || seconds <= 0) {
    return "-";
  }
  const d = new Date(seconds * 1000);
  if (Number.isNaN(d.getTime())) {
    return "-";
  }
  const pad = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

function formatTitle(v) {
  if (v.PageIndex > 1) {
    const part = v.Part ? ` ${v.Part}` : "";
    return `${v.Title || v.Bvid}_P${v.PageIndex}${part}`;
  }
  return v.Title || v.Bvid || "(无标题)";
}

function renderTable(items) {
  const tbody = $("videos-body");
  tbody.textContent = "";
  if (!Array.isArray(items) || items.length === 0) {
    const row = tbody.insertRow();
    const cell = row.insertCell();
    cell.colSpan = 7;
    cell.className = "empty";
    cell.textContent = "暂无视频，请添加视频";
    return;
  }
  items.forEach((v, i) => {
    const row = tbody.insertRow();
    row.insertCell().textContent = pageOffset + i + 1;
    row.insertCell().textContent = formatTitle(v);
    row.insertCell().textContent = v.Author || "-";
    row.insertCell().textContent = formatPubdate(v.Pubdate);
    const state = row.insertCell();
    state.textContent = STATE_LABELS[v.State] || v.State || "-";
    state.className = `state state-${v.State || "unknown"}`;
    row.insertCell().textContent = v.Lang || "-";
    row.insertCell().textContent = v.IsCharging ? "是" : "否";
  });
}

async function loadVideos() {
  try {
    // The list accumulates every probed source on purpose: this is a batch
    // collector, so a new probe appends instead of replacing. 清空数据 resets it.
    const params = new URLSearchParams();
    params.set("offset", String(pageOffset));
    params.set("limit", String(PAGE_SIZE));
    const data = await api(`/api/videos?${params.toString()}`);
    pageTotal = data.total;
    // The filter can change under us (显示全部来源, 清空数据, a remote reset).
    // If the current page is now past the end, fall back to the last real page
    // instead of rendering an empty table with a reversed range.
    if (data.items.length === 0 && pageTotal > 0 && pageOffset > 0) {
      pageOffset = Math.floor((pageTotal - 1) / PAGE_SIZE) * PAGE_SIZE;
      return loadVideos();
    }
    renderTable(data.items);
    updatePager();
    await loadStats();
  } catch (err) {
    log(`读取视频列表失败：${err.message}`);
    renderTable([]);
  }
}

// updatePager mirrors pageOffset/pageTotal into the pager under the table and
// disables an end button when there is nothing further to step to.
function updatePager() {
  const info = $("page-info");
  if (pageTotal === 0) {
    info.textContent = "共 0 条";
  } else {
    const start = Math.min(pageOffset + 1, pageTotal);
    const end = Math.min(pageOffset + PAGE_SIZE, pageTotal);
    info.textContent = `第 ${start}–${end} 条 / 共 ${pageTotal} 条`;
  }
  $("page-prev").disabled = pageOffset === 0;
  $("page-next").disabled = pageOffset + PAGE_SIZE >= pageTotal;
}

async function loadStats() {
  try {
    const stats = await api("/api/stats");
    const parts = Object.keys(stats || {}).map((k) => `${STATE_LABELS[k] || k} ${stats[k]}`);
    $("stats").textContent = parts.length ? `（${parts.join("，")}）` : "";
  } catch (err) {
    log(`读取统计失败：${err.message}`);
  }
}

async function probe() {
  const url = $("url").value.trim();
  if (!url) {
    log("请输入链接再获取列表。");
    return;
  }
  const button = $("probe");
  const label = button.textContent;
  button.disabled = true;
  button.textContent = "获取中…";
  beginProgress("probe");
  log(`开始获取视频列表：${url}`);
  try {
    const summary = await post("/api/probe", {
      url,
      kind: $("kind").value,
      force: $("force-probe").checked,
    });
    pageOffset = 0;
    if (summary.probed === 0) {
      log(`获取完成：该来源没有视频（来源 ${summary.kind || "?"} ${summary.source || ""}）。`);
    } else {
      log(`已列出 ${summary.probed} 条。点「获取字幕」会检查并下载；没字幕的会标为「无字幕」，之后可点「转写无字幕」。`);
    }
    // One video can be one episode of a 合集. The server decides that and sends
    // the sentence (with the link to paste) rather than expanding it here: a
    // 1-video request must not quietly turn into 31.
    if (summary.hint) {
      log(summary.hint);
    }
    // A whole-leg stop (an expired session) or a per-video failure reason. The
    // server carries it on the summary because the CLI's own writer is discarded
    // in serve mode, so the browser would otherwise see only the failure count.
    if (summary.reason) {
      log(`注意：${summary.reason}`);
    }
    await loadVideos();
  } catch (err) {
    showFailure();
    log(`获取失败：${err.message}`);
  } finally {
    button.disabled = false;
    button.textContent = label;
  }
}

async function download() {
  log("开始获取字幕…");
  beginProgress("download");
  try {
    const summary = await post("/api/download", {
      lang: $("lang").value,
      transcribe: $("transcribe").checked,
      asr_lang: $("asr-lang").value,
      model: $("asr-model").value.trim(),
    });
    log(describeDownload(summary));
    if (summary.error) {
      // Part-way failure: the counts above are real, so this is a warning
      // rather than a failure, and the progress bar must not go red.
      log(`注意：${summary.error}`);
    }
    await loadVideos();
  } catch (err) {
    showFailure();
    log(`获取字幕失败：${err.message}`);
  }
}

// describeDownload reports what a download run accomplished. The transcribed
// counters only move when the ASR leg ran, so they are appended rather than
// always shown. no_subtitle/charging_locked are classifications the download
// step discovers now that the list step no longer checks subtitles.
function describeDownload(summary) {
  let text = `获取字幕完成：成功 ${summary.fetched}`;
  if (summary.no_subtitle) {
    text += `，无字幕 ${summary.no_subtitle}`;
  }
  if (summary.charging_locked) {
    text += `，充电专属 ${summary.charging_locked}`;
  }
  text += `，失败 ${summary.failed}`;
  if (summary.transcribed || summary.transcribe_failed) {
    text += `；转写成功 ${summary.transcribed || 0}，失败 ${summary.transcribe_failed || 0}`;
  }
  return text;
}

// describeTranscribe reports what a standalone ASR run accomplished.
function describeTranscribe(summary) {
  if (!summary.total) {
    return "没有需要转写的无字幕视频";
  }
  return `成功 ${summary.transcribed || 0}，失败 ${summary.failed || 0}`;
}

// transcribeNoSubtitle runs the ASR leg on its own, for videos that were already
// probed as no_subtitle but not transcribed (the caller did not check
// 「无字幕时转写」at download time).
async function transcribeNoSubtitle() {
  const asrLang = $("asr-lang").value;
  const model = $("asr-model").value.trim();
  log(`开始转写无字幕视频（语言 ${asrLang || "自动"}${model ? "，模型 " + model : ""}）…`);
  beginProgress("transcribe");
  try {
    const summary = await post("/api/transcribe", { asr_lang: asrLang, model });
    log(`转写完成：${describeTranscribe(summary)}`);
    if (summary.error) {
      log(`注意：${summary.error}`);
    }
    await loadVideos();
  } catch (err) {
    showFailure();
    log(`转写失败：${err.message}`);
  }
}

// selectedFormats is fixed to txt: the export is meant to be a plain-text
// reading copy. The server still accepts srt/vtt/ass/json/md, so other formats
// remain one API call (or one --format) away without touching the pipeline.
function selectedFormats() {
  return ["txt"];
}

async function exportZip() {
  const formats = selectedFormats();
  const includeExported = $("include-exported").checked;
  log(`开始导出（${formats.join(",")}${includeExported ? "，含已导出" : ""}）…`);
  beginProgress("export");
  try {
    const result = await post("/api/export", { formats, include_exported: includeExported });
    if (!result.zip) {
      log(result.message || "没有尚未导出的字幕。");
      $("progress-label").textContent = "无新内容";
      $("progress-fill").style.width = "0%";
      return;
    }
    log(`导出完成：${result.zip}`);
    if (!includeExported) {
      log("本次导出的视频已标记为「已导出」，下次默认不再打包；如需重新导出请勾选「包含已导出的」。");
    }
  } catch (err) {
    showFailure();
    log(`导出失败：${err.message}`);
  }
}

// looksLikeCookie mirrors bili.LooksLikeCookie: a full browser Cookie header
// carries SESSDATA= or more than one ';'-separated key=value pair. The server
// derives SESSDATA from it, so the page only has to pick the right field.
function looksLikeCookie(value) {
  if (value.includes("SESSDATA=")) {
    return true;
  }
  const pairs = value
    .split(";")
    .map((part) => part.trim())
    .filter((part) => {
      const eq = part.indexOf("=");
      return eq > 0;
    });
  return pairs.length > 1;
}

async function saveSessdata() {
  const value = $("sessdata").value.trim();
  if (!value) {
    log("请先粘贴 SESSDATA 或完整 Cookie。");
    return;
  }
  const body = looksLikeCookie(value) ? { cookie: value } : { sessdata: value };
  try {
    await post("/api/login/sessdata", body);
    $("sessdata").value = "";
    $("login-status").textContent = "登录状态：已保存登录信息";
    log("登录信息已保存。");
  } catch (err) {
    log(`保存登录信息失败：${err.message}`);
  }
}

async function qrLogin() {
  const area = $("qr-area");
  const status = $("qr-status");
  try {
    const result = await api("/api/login/qrcode");
    area.hidden = false;
    $("qr-img").src = result.png;
    status.textContent = "请用哔哩哔哩 App 扫码，并在手机上确认登录";
    log("二维码已生成，请用哔哩哔哩 App 扫描。");
    startQRStatusPolling();
  } catch (err) {
    area.hidden = true;
    log(`获取二维码失败：${err.message}`);
  }
}

// The QR flow finishes in a background poll on the server, so the page learns
// about it two ways: the SSE `login` event, and this poll of
// /api/login/status. The poll also makes the state survive a page refresh.
let qrPollTimer = null;

function startQRStatusPolling() {
  stopQRStatusPolling();
  qrPollTimer = setInterval(loadLoginStatus, 2000);
}

function stopQRStatusPolling() {
  if (qrPollTimer) {
    clearInterval(qrPollTimer);
    qrPollTimer = null;
  }
}

async function loadLoginStatus() {
  try {
    const state = await api("/api/login/status");
    applyLoginStatus(state);
  } catch (err) {
    // The status endpoint is optional; the rest of the page still works.
  }
}

function applyLoginStatus(state) {
  const el = $("login-status");
  if (state && state.logged_in) {
    el.textContent = `登录状态：已登录${state.uname ? `（${state.uname}）` : ""}`;
    el.classList.add("ok");
    stopQRStatusPolling();
    $("qr-area").hidden = true;
    return;
  }
  el.textContent = "登录状态：未登录";
  el.classList.remove("ok");
}

// clearData drops every stored video/transcript, then reloads the now-empty
// list. It is confirm-gated because the action cannot be undone.
async function clearData() {
  if (!confirm("确定清空所有已获取/已下载的数据吗？此操作不可恢复。")) {
    return;
  }
  try {
    const result = await post("/api/reset", { confirm: true });
    log(`已清空数据：删除 ${result.deleted} 条记录。`);
    pageOffset = 0;
    await loadVideos();
  } catch (err) {
    log(`清空数据失败：${err.message}`);
  }
}

function formatEvent(ev) {
  const data = ev.data || {};
  switch (ev.type) {
    case "probe":
      return `获取事件：已列出 ${data.probed} 条`;
    case "download":
      return `下载事件：${describeDownload(data)}`;
    case "transcribe":
      return `转写事件：${describeTranscribe(data)}`;
    case "export":
      return `导出事件：${data.zip}`;
    case "reset":
      return `数据已清空：删除 ${data.deleted} 条记录`;
    case "login":
      return data.error ? `扫码登录失败：${data.error}` : "扫码登录成功，已保存 SESSDATA。";
    case "handoff": {
      const parts = [];
      if (data.url) {
        parts.push("已填入插件发来的链接，请点「获取视频列表」");
      }
      if (data.logged_in) {
        parts.push(`登录成功${data.uname ? `（${data.uname}）` : ""}`);
      } else if (data.error) {
        parts.push(`登录失败：${data.error}`);
      }
      return parts.length ? `浏览器插件：${parts.join("，")}` : "浏览器插件：已收到";
    }
    default:
      return `事件 ${ev.type}：${JSON.stringify(data)}`;
  }
}

// connectEvents subscribes to the server's SSE stream. EventSource reconnects
// on its own if the connection drops.
function connectEvents() {
  const source = new EventSource("/api/events");
  source.onmessage = (e) => {
    try {
      const ev = JSON.parse(e.data);
      if (ev.type === "progress") {
        const data = ev.data || {};
        showProgress(data.op, data.done, data.total);
        return;
      }
      log(formatEvent(ev));
      if (ev.type === "login") {
        const ok = !!(ev.data && ev.data.ok);
        const status = $("qr-status");
        if (ok) {
          if (status) {
            status.textContent = "登录成功，已保存 SESSDATA。";
          }
          applyLoginStatus({ logged_in: true });
          loadLoginStatus();
          log("扫码登录成功。");
        } else if (ev.data && ev.data.error) {
          if (status) {
            status.textContent = `登录失败：${ev.data.error}`;
          }
          stopQRStatusPolling();
          log(`扫码登录失败：${ev.data.error}`);
        }
      }
      if (ev.type === "handoff") {
        const data = ev.data || {};
        if (data.url) {
          $("url").value = data.url;
        }
        if (data.logged_in) {
          applyLoginStatus({ logged_in: true, uname: data.uname });
        }
      }
      if (ev.type === "probe" || ev.type === "download" || ev.type === "export") {
        // The operation is done: fill the bar and keep it visible with a
        // completion label instead of hiding it.
        $("progress-label").textContent = `${OP_LABELS[ev.type]}完成`;
        $("progress-fill").style.width = "100%";
        $("progress-panel").hidden = false;
        if (ev.type === "probe" || ev.type === "download") {
          loadVideos();
        }
      }
      if (ev.type === "reset") {
        // Another page cleared the store; reload whatever this page is showing.
        loadVideos();
      }
    } catch (err) {
      log(`事件解析失败：${e.data}`);
    }
  };
  source.onerror = () => {
    // The browser retries automatically; surface it once without spamming.
    if (!connectEvents.warned) {
      connectEvents.warned = true;
      log("SSE 连接中断，浏览器会自动重连。");
    }
  };
}

$("probe").addEventListener("click", probe);
$("download").addEventListener("click", download);
$("transcribe-only").addEventListener("click", transcribeNoSubtitle);
// The ASR controls only matter when the transcribe box is ticked.
function syncTranscribeControls() {
  const on = $("transcribe").checked;
  $("asr-lang").disabled = !on;
  $("asr-model").disabled = !on;
}
$("transcribe").addEventListener("change", syncTranscribeControls);
syncTranscribeControls();
$("export").addEventListener("click", exportZip);
$("refresh").addEventListener("click", loadVideos);
$("clear-data").addEventListener("click", clearData);
$("page-prev").addEventListener("click", () => {
  pageOffset = Math.max(0, pageOffset - PAGE_SIZE);
  loadVideos();
});
$("page-next").addEventListener("click", () => {
  if (pageOffset + PAGE_SIZE >= pageTotal) {
    return;
  }
  pageOffset += PAGE_SIZE;
  loadVideos();
});
$("save-sessdata").addEventListener("click", saveSessdata);
$("qr-login").addEventListener("click", qrLogin);

loadVideos();
connectEvents();
loadLoginStatus();
