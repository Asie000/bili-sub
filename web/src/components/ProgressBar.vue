<script setup>
import { state } from "../lib/store.js";
</script>

<template>
  <!-- The panel is never hidden on completion, so progress stays visible. -->
  <div v-show="state.progress.visible" class="progress-panel">
    <span class="progress-label">{{ state.progress.label }}</span>
    <div class="progress-bar">
      <div class="progress-fill" :style="{ width: `${state.progress.percent}%` }"></div>
    </div>
  </div>
</template>
