"use strict";

// The page talks to the JSON API in internal/server/http.go. /api/videos
// marshals store.Video directly, so the JSON keys are the Go field names
// (Title, Author, Pubdate, State, Lang, IsCharging, ...).

const $ = (id) => document.getElementById(id);

const STATE_LABELS = {
  pending: "待处理",
  probed: "已探测",
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

function renderTable(videos) {
  const tbody = $("videos-body");
  tbody.textContent = "";
  if (!Array.isArray(videos) || videos.length === 0) {
    const row = tbody.insertRow();
    const cell = row.insertCell();
    cell.colSpan = 7;
    cell.className = "empty";
    cell.textContent = "暂无视频，请添加视频";
    return;
  }
  videos.forEach((v, i) => {
    const row = tbody.insertRow();
    row.insertCell().textContent = i + 1;
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
    const videos = await api("/api/videos");
    renderTable(videos);
    await loadStats();
  } catch (err) {
    log(`读取视频列表失败：${err.message}`);
    renderTable([]);
  }
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
    log("请输入链接再探测。");
    return;
  }
  log(`开始探测：${url}`);
  try {
    const summary = await post("/api/probe", { url });
    log(`探测完成：共 ${summary.probed}，可下载 ${summary.downloadable}，无字幕 ${summary.no_subtitle}，充电锁定 ${summary.charging_locked}，失败 ${summary.failed}`);
    await loadVideos();
  } catch (err) {
    log(`探测失败：${err.message}`);
  }
}

async function download() {
  log("开始获取字幕…");
  try {
    const summary = await post("/api/download", {
      lang: $("lang").value,
      transcribe: $("transcribe").checked,
    });
    log(`获取字幕完成：成功 ${summary.fetched}，失败 ${summary.failed}`);
    await loadVideos();
  } catch (err) {
    log(`获取字幕失败：${err.message}`);
  }
}

// selectedFormats reads the format checkboxes the page offers. The server
// rejects an empty list, so exportZip reports instead of posting one.
function selectedFormats() {
  return Array.from(document.querySelectorAll(".export-format"))
    .filter((box) => box.checked)
    .map((box) => box.value);
}

async function exportZip() {
  const formats = selectedFormats();
  if (formats.length === 0) {
    log("请至少选择一种导出格式。");
    return;
  }
  log(`开始导出（${formats.join(",")}）…`);
  try {
    const result = await post("/api/export", { formats });
    log(`导出完成：${result.zip}`);
  } catch (err) {
    log(`导出失败：${err.message}`);
  }
}

async function saveSessdata() {
  const sessdata = $("sessdata").value;
  if (!sessdata) {
    log("请先粘贴 SESSDATA。");
    return;
  }
  try {
    await post("/api/login/sessdata", { sessdata });
    $("sessdata").value = "";
    $("login-status").textContent = "登录状态：已保存 SESSDATA";
    log("SESSDATA 已保存。");
  } catch (err) {
    log(`保存 SESSDATA 失败：${err.message}`);
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

// buildBookmarklet turns the current origin into a javascript: bookmark. Clicking
// it on a bilibili page POSTs that page's URL to this tool. The endpoint allows
// exactly bilibili origins through CORS, so no extension is needed.
function buildBookmarklet() {
  const origin = location.origin;
  const code =
    "(function(){var u=location.href;" +
    "fetch(" + JSON.stringify(origin + "/api/probe") + ",{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:u})})" +
    ".then(function(r){return r.json()})" +
    ".then(function(d){if(d&&d.error){alert('bili-sub 探测失败：'+d.error)}else{alert('bili-sub 已探测：共 '+d.probed+'，可下载 '+d.downloadable+'，无字幕 '+d.no_subtitle+'，失败 '+d.failed)}})" +
    ".catch(function(e){alert('bili-sub 请求失败：'+e)});})()";
  const link = $("bookmarklet");
  if (!link) {
    return;
  }
  link.href = "javascript:" + encodeURIComponent(code);
  link.addEventListener("click", (e) => {
    // On this page a click would probe the tool's own URL; ask for a drag.
    e.preventDefault();
    log("请把「拖我到书签栏」拖进浏览器书签栏，然后在 B站 页面点击它。");
  });
}

function clearTable() {
  $("videos-body").textContent = "";
  $("stats").textContent = "";
  $("log").textContent = "";
  log("已清空列表与进度。");
}

function formatEvent(ev) {
  const data = ev.data || {};
  switch (ev.type) {
    case "probe":
      return `探测事件：共 ${data.probed}，可下载 ${data.downloadable}，无字幕 ${data.no_subtitle}，充电锁定 ${data.charging_locked}，失败 ${data.failed}`;
    case "download":
      return `下载事件：成功 ${data.fetched}，失败 ${data.failed}`;
    case "export":
      return `导出事件：${data.zip}`;
    case "login":
      return data.error ? `扫码登录失败：${data.error}` : "扫码登录成功，已保存 SESSDATA。";
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
      if (ev.type === "probe" || ev.type === "download") {
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
$("export").addEventListener("click", exportZip);
$("refresh").addEventListener("click", loadVideos);
$("clear").addEventListener("click", clearTable);
$("save-sessdata").addEventListener("click", saveSessdata);
$("qr-login").addEventListener("click", qrLogin);

loadVideos();
connectEvents();
loadLoginStatus();
buildBookmarklet();
