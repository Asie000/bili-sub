<script setup>
import { nextTick, ref, watch } from "vue";
import { state } from "../lib/store.js";

const box = ref(null);

// The log is append-only and always scrolled to the newest line, so a long run
// never leaves the reader looking at the top of the history.
watch(
  () => state.logs.length,
  async () => {
    await nextTick();
    if (box.value) {
      box.value.scrollTop = box.value.scrollHeight;
    }
  },
);
</script>

<template>
  <section class="panel">
    <h2>进度</h2>
    <pre ref="box" class="log">{{ state.logs.join("\n") }}</pre>
  </section>
</template>
