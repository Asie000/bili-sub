import { reactive } from "vue";
import { api, post } from "./api.js";
import { OP_LABELS, STATE_LABELS } from "./labels.js";
import { describeDownload, describeTranscribe } from "./describe.js";
import { looksLikeCookie } from "./summary.js";

// PAGE_SIZE is the fixed page size the server accepts.
export const PAGE_SIZE = 100;

// state is the whole page. It is a plain reactive object instead of a store
// library: this page has one screen and one reader, so a router, actions layer
// and devtools integration would be more moving parts than the UI is worth.
export const state = reactive({
  form: {
    kind: "",
    url: "",
    lang: "",
    transcribe: false,
    asrLang: "zh",
    model: "",
    includeExported: false,
    forceProbe: false,
    sessdata: "",
  },
  videos: [],
  total: 0,
  offset: 0,
  statsText: "",
  pager: { text: "共 0 条", canPrev: false, canNext: false },
  logs: [],
  progress: { visible: false, label: "", percent: 0 },
  login: { text: "登录状态：未设置 SESSDATA", ok: false },
  qr: { visible: false, png: "", status: "", pending: false },
  // One flag per long-running operation: the button that starts an operation is
  // disabled while it runs, so a second click cannot start the same run twice.
  busy: { probe: false, download: false, transcribe: false, export: false, login: false },
});

export function log(message) {
  const time = new Date().toLocaleTimeString();
  state.logs.push(`[${time}] ${message}`);
}

// showProgress fills the persistent panel under the header. A tick with total 0
// (an operation that reports no count) shows 0% and a done/0 label. The panel is
// never hidden on completion, so progress stays visible.
export function showProgress(op, done, total) {
  state.progress.visible = true;
  state.progress.label = `${OP_LABELS[op] || op} ${done}/${total}`;
  state.progress.percent = total > 0 ? (done / total) * 100 : 0;
}

// showFailure keeps the panel visible after a failed operation instead of
// hiding it, so the bar does not silently disappear.
export function showFailure() {
  state.progress.visible = true;
  state.progress.label = "失败";
  state.progress.percent = 0;
}

// beginProgress shows the bar for a locally started operation before the
// server's first tick arrives.
export function beginProgress(op) {
  showProgress(op, 0, 0);
  state.progress.label = "准备中…";
}

// markDone fills the bar when an operation reports completion without a final
// 100% tick.
export function markDone(op) {
  state.progress.visible = true;
  state.progress.label = `${OP_LABELS[op] || op}完成`;
  state.progress.percent = 100;
}

// updatePager mirrors offset/total into the pager under the table and disables
// an end button when there is nothing further to step to.
function updatePager() {
  const total = state.total;
  if (total === 0) {
    state.pager.text = "共 0 条";
  } else {
    const start = Math.min(state.offset + 1, total);
    const end = Math.min(state.offset + PAGE_SIZE, total);
    state.pager.text = `第 ${start}–${end} 条 / 共 ${total} 条`;
  }
  state.pager.canPrev = state.offset > 0;
  state.pager.canNext = state.offset + PAGE_SIZE < total;
}

// loadSeq numbers the list requests. Only the newest one may write the table:
// a slow answer for page 1 must not land on top of a fresh page 2, which would
// render rows from one page with 序号 from another.
let loadSeq = 0;

export async function loadVideos() {
  const seq = ++loadSeq;
  // The page this request is for, frozen: state.offset can move while the
  // answer is in flight.
  const offset = state.offset;
  try {
    // The list accumulates every probed source on purpose: this is a batch
    // collector, so a new probe appends instead of replacing. 清空数据 resets it.
    const params = new URLSearchParams();
    params.set("offset", String(offset));
    params.set("limit", String(PAGE_SIZE));
    const data = await api(`/api/videos?${params.toString()}`);
    if (seq !== loadSeq) {
      return;
    }
    state.total = data.total;
    const items = Array.isArray(data.items) ? data.items : [];
    // The filter can change under us (清空数据, a remote reset). If the requested
    // page is now past the end, fall back to the last real page instead of
    // rendering an empty table with a reversed range.
    if (items.length === 0 && state.total > 0 && offset > 0) {
      const lastOffset = Math.floor((state.total - 1) / PAGE_SIZE) * PAGE_SIZE;
      // One step back at most. Retrying the same offset would ask forever: the
      // server counts rows and lists them in two separate statements, so it can
      // report a total whose last page it then hands back empty.
      if (lastOffset !== offset) {
        state.offset = lastOffset;
        return loadVideos();
      }
      state.videos = [];
      updatePager();
      log(`列表对不上：服务端报告共 ${state.total} 条，但第 ${offset + 1} 条起是空的，已停在这里。点「刷新」重试。`);
      return;
    }
    state.videos = items;
    updatePager();
    await loadStats();
  } catch (err) {
    if (seq !== loadSeq) {
      return;
    }
    log(`读取视频列表失败：${err.message}`);
    // Forget the old page entirely, so the empty table and the pager agree.
    state.videos = [];
    state.total = 0;
    updatePager();
  }
}

async function loadStats() {
  try {
    const stats = await api("/api/stats");
    const parts = Object.keys(stats || {}).map((k) => `${STATE_LABELS[k] || k} ${stats[k]}`);
    state.statsText = parts.length ? `（${parts.join("，")}）` : "";
  } catch (err) {
    log(`读取统计失败：${err.message}`);
  }
}

export function gotoPage(delta) {
  state.offset = Math.max(0, state.offset + delta * PAGE_SIZE);
  return loadVideos();
}

export async function probe() {
  const url = state.form.url.trim();
  if (!url) {
    log("请输入链接再获取列表。");
    return;
  }
  if (state.busy.probe) {
    return;
  }
  state.busy.probe = true;
  beginProgress("probe");
  log(`开始获取视频列表：${url}`);
  try {
    const summary = await post("/api/probe", {
      url,
      kind: state.form.kind,
      force: state.form.forceProbe,
    });
    state.offset = 0;
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
    state.busy.probe = false;
  }
}

export async function download() {
  if (state.busy.download) {
    return;
  }
  state.busy.download = true;
  log("开始获取字幕…");
  beginProgress("download");
  try {
    const summary = await post("/api/download", {
      lang: state.form.lang,
      transcribe: state.form.transcribe,
      asr_lang: state.form.asrLang,
      model: state.form.model.trim(),
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
  } finally {
    state.busy.download = false;
  }
}

// transcribeNoSubtitle runs the ASR leg on its own, for videos that were already
// probed as no_subtitle but not transcribed (the caller did not check
// 「无字幕时转写」at download time).
export async function transcribeNoSubtitle() {
  if (state.busy.transcribe) {
    return;
  }
  const asrLang = state.form.asrLang;
  const model = state.form.model.trim();
  state.busy.transcribe = true;
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
  } finally {
    state.busy.transcribe = false;
  }
}

export async function exportZip() {
  if (state.busy.export) {
    return;
  }
  // Fixed to txt: the export is meant to be a plain-text reading copy. The
  // server still accepts srt/vtt/ass/json/md, so other formats remain one API
  // call (or one --format) away without touching the pipeline.
  const formats = ["txt"];
  const includeExported = state.form.includeExported;
  state.busy.export = true;
  log(`开始导出（${formats.join(",")}${includeExported ? "，含已导出" : ""}）…`);
  beginProgress("export");
  try {
    const result = await post("/api/export", { formats, include_exported: includeExported });
    if (!result.zip) {
      log(result.message || "没有尚未导出的字幕。");
      state.progress.visible = true;
      state.progress.label = "无新内容";
      state.progress.percent = 0;
      return;
    }
    log(`导出完成：${result.zip}`);
    if (!includeExported) {
      log("本次导出的视频已标记为「已导出」，下次默认不再打包；如需重新导出请勾选「包含已导出的」。");
    }
  } catch (err) {
    showFailure();
    log(`导出失败：${err.message}`);
  } finally {
    state.busy.export = false;
  }
}

export async function saveSessdata() {
  const value = state.form.sessdata.trim();
  if (!value) {
    log("请先粘贴 SESSDATA 或完整 Cookie。");
    return;
  }
  // One save at a time: two in flight would write the session file twice, and
  // the written file is only replaced by truncate-and-write, not by a rename.
  if (state.busy.login) {
    return;
  }
  state.busy.login = true;
  try {
    const body = looksLikeCookie(value) ? { cookie: value } : { sessdata: value };
    await post("/api/login/sessdata", body);
    state.form.sessdata = "";
    state.login.text = "登录状态：已保存登录信息";
    log("登录信息已保存。");
  } catch (err) {
    log(`保存登录信息失败：${err.message}`);
  } finally {
    state.busy.login = false;
  }
}

// The QR flow finishes in a background poll on the server, so the page learns
// about it two ways: the SSE `login` event, and this poll of /api/login/status.
// The poll also makes the state survive a page refresh.
let qrPollTimer = null;

// qrStartedAt bounds the poll the way the server bounds its own goroutine
// (qrLoginTimeout, 5 minutes): after that the code is long expired and nothing
// should keep asking.
const QR_LOGIN_TIMEOUT_MS = 5 * 60 * 1000;
let qrStartedAt = 0;

function startQRStatusPolling() {
  // Replace any running timer without touching state.qr.pending, which is what
  // the caller just set: only stopQRStatusPolling ends a scan attempt.
  if (qrPollTimer) {
    clearInterval(qrPollTimer);
  }
  qrStartedAt = Date.now();
  qrPollTimer = setInterval(() => {
    if (Date.now() - qrStartedAt > QR_LOGIN_TIMEOUT_MS) {
      state.qr.status = "二维码已过期，请重新点「扫码登录」。";
      state.qr.visible = false;
      stopQRStatusPolling();
      return;
    }
    loadLoginStatus();
  }, 2000);
}

// stopQRStatusPolling ends a scan attempt: it stops the poll and re-enables the
// button. Every way an attempt can end goes through here (success, failure,
// expiry), so state.qr.pending cannot be left stuck.
function stopQRStatusPolling() {
  if (qrPollTimer) {
    clearInterval(qrPollTimer);
    qrPollTimer = null;
  }
  state.qr.pending = false;
}

export async function qrLogin() {
  // One waiting scan at a time: every extra call leaves a 5-minute background
  // poll on the server, each spending its own request budget and each able to
  // overwrite the saved session.
  if (state.qr.pending) {
    return;
  }
  state.qr.pending = true;
  try {
    const result = await api("/api/login/qrcode");
    state.qr.visible = true;
    state.qr.png = result.png;
    state.qr.status = "请用哔哩哔哩 App 扫码，并在手机上确认登录";
    log("二维码已生成，请用哔哩哔哩 App 扫描。");
    startQRStatusPolling();
  } catch (err) {
    state.qr.visible = false;
    state.qr.pending = false;
    log(`获取二维码失败：${err.message}`);
  }
}

export async function loadLoginStatus() {
  try {
    applyLoginStatus(await api("/api/login/status"));
  } catch {
    // The status endpoint is optional; the rest of the page still works.
  }
}

export function applyLoginStatus(status) {
  if (status && status.logged_in) {
    state.login.text = `登录状态：已登录${status.uname ? `（${status.uname}）` : ""}`;
    state.login.ok = true;
    stopQRStatusPolling();
    state.qr.visible = false;
    return;
  }
  state.login.text = "登录状态：未登录";
  state.login.ok = false;
}

// clearData drops every stored video/transcript, then reloads the now-empty
// list. It is confirm-gated because the action cannot be undone.
export async function clearData() {
  if (!confirm("确定清空所有已获取/已下载的数据吗？此操作不可恢复。")) {
    return;
  }
  try {
    const result = await post("/api/reset", { confirm: true });
    log(`已清空数据：删除 ${result.deleted} 条记录。`);
    state.offset = 0;
    await loadVideos();
  } catch (err) {
    log(`清空数据失败：${err.message}`);
  }
}

// stopQRStatusPolling is exported for the SSE handler, which stops the poll as
// soon as the server reports the login finished.
export { stopQRStatusPolling };
