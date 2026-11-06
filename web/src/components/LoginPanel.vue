<script setup>
import { qrLogin, saveSessdata, state } from "../lib/store.js";
</script>

<template>
  <section class="panel">
    <h2>登录 / Cookie</h2>
    <div class="row">
      <input
        v-model="state.form.sessdata"
        type="password"
        autocomplete="off"
        placeholder="粘贴 SESSDATA 或完整 Cookie"
      />
      <button type="button" :disabled="state.busy.login" @click="saveSessdata">保存登录信息</button>
      <button type="button" :disabled="state.qr.pending" @click="qrLogin">扫码登录</button>
    </div>
    <p class="hint guide">
      从 B站页面 F12 → 网络 → 任意请求 → 请求头 Cookie，整段复制（推荐，能避免 412）。
    </p>
    <div v-show="state.qr.visible" class="qr-area">
      <img :src="state.qr.png" alt="登录二维码" width="240" height="240" />
      <div class="hint">{{ state.qr.status }}</div>
    </div>
  </section>
</template>
