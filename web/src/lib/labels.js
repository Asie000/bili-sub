// Display vocabulary shared by the table, the progress panel and the SSE log.

export const STATE_LABELS = {
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

// OP_LABELS names the long-running operations for the progress panel.
export const OP_LABELS = { probe: "获取列表", download: "获取字幕", transcribe: "转写", export: "导出" };

export function stateLabel(state) {
  return STATE_LABELS[state] || state || "-";
}

export function formatPubdate(seconds) {
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

// formatTitle appends the part number, because one bvid can be multi-page and
// the list would otherwise show the same title several times.
export function formatTitle(v) {
  if (v.PageIndex > 1) {
    const part = v.Part ? ` ${v.Part}` : "";
    return `${v.Title || v.Bvid}_P${v.PageIndex}${part}`;
  }
  return v.Title || v.Bvid || "(无标题)";
}
