<script setup>
import { onBeforeUnmount, onMounted } from "vue";
import LoginPanel from "./components/LoginPanel.vue";
import LogPanel from "./components/LogPanel.vue";
import ProgressBar from "./components/ProgressBar.vue";
import SourcePanel from "./components/SourcePanel.vue";
import VideoTable from "./components/VideoTable.vue";
import { connectEvents } from "./lib/events.js";
import { loadLoginStatus, loadVideos, state } from "./lib/store.js";

// One SSE connection for the whole page: the log, the progress bar and the
// table all read from the same stream.
let source = null;

onMounted(() => {
  loadVideos();
  source = connectEvents();
  loadLoginStatus();
});

onBeforeUnmount(() => {
  if (source) {
    source.close();
  }
});
</script>

<template>
  <header>
    <h1>bili-sub 字幕采集</h1>
    <div class="status" :class="{ ok: state.login.ok }">{{ state.login.text }}</div>
  </header>

  <ProgressBar />

  <main>
    <LoginPanel />
    <SourcePanel />
    <VideoTable />
    <LogPanel />
  </main>
</template>
