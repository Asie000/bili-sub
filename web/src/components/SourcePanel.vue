<script setup>
import {
  download,
  exportZip,
  clearData,
  loadVideos,
  probe,
  state,
  transcribeNoSubtitle,
} from "../lib/store.js";
</script>

<template>
  <section class="panel">
    <h2>来源</h2>
    <div class="row">
      <select v-model="state.form.kind">
        <option value="">自动识别</option>
        <option value="single">单个视频</option>
        <option value="space">UP主主页</option>
        <option value="fav">收藏夹</option>
        <option value="season">合集</option>
        <option value="search">搜索关键词</option>
      </select>
      <input
        v-model="state.form.url"
        type="text"
        placeholder="视频(BV/av/b23.tv) / UP主 / 收藏夹 / 合集 链接；选「搜索关键词」时填关键词"
      />
      <button type="button" :disabled="state.busy.probe" @click="probe">
        {{ state.busy.probe ? "获取中…" : "获取视频列表" }}
      </button>
    </div>

    <div class="row">
      <label for="lang">字幕语言</label>
      <select id="lang" v-model="state.form.lang">
        <option value="">默认</option>
        <option value="zh">中文</option>
        <option value="en">英文</option>
      </select>

      <label class="checkbox"><input v-model="state.form.transcribe" type="checkbox" /> 无字幕时转写</label>
      <label for="asr-lang">转写语言</label>
      <select id="asr-lang" v-model="state.form.asrLang" :disabled="!state.form.transcribe">
        <option value="zh">中文</option>
        <option value="en">英文</option>
        <option value="">自动检测</option>
      </select>
      <input
        v-model="state.form.model"
        type="text"
        size="14"
        placeholder="模型（默认按 .env）"
        :disabled="!state.form.transcribe"
      />

      <button type="button" :disabled="state.busy.download" @click="download">获取字幕</button>
      <button type="button" :disabled="state.busy.transcribe" @click="transcribeNoSubtitle">转写无字幕</button>
      <button type="button" :disabled="state.busy.export" @click="exportZip">导出</button>

      <label class="checkbox"><input v-model="state.form.includeExported" type="checkbox" /> 包含已导出的</label>
      <button type="button" @click="loadVideos">刷新</button>
      <button type="button" @click="clearData">清空</button>
      <label class="checkbox"><input v-model="state.form.forceProbe" type="checkbox" /> 重新获取已完成的</label>
    </div>
  </section>
</template>
