// describeDownload and describeTranscribe report what one operation
// accomplished. They are shared by the request handler and the SSE log, so the
// same run reads the same whether it was started here or in another tab.

// describeDownload reports what a download run accomplished. The transcribed
// counters only move when the ASR leg ran, so they are appended rather than
// always shown. no_subtitle/charging_locked are classifications the download
// step discovers now that the list step no longer checks subtitles.
export function describeDownload(summary) {
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
export function describeTranscribe(summary) {
  if (!summary.total) {
    return "没有需要转写的无字幕视频";
  }
  return `成功 ${summary.transcribed || 0}，失败 ${summary.failed || 0}`;
}
