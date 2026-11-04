<script setup>
import { formatPubdate, formatTitle, stateLabel } from "../lib/labels.js";
import { gotoPage, state } from "../lib/store.js";
</script>

<template>
  <section class="panel">
    <h2>视频列表 <span class="hint">{{ state.statsText }}</span></h2>
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>序号</th>
            <th>标题</th>
            <th>作者</th>
            <th>发布日期</th>
            <th>字幕状态</th>
            <th>字幕语言</th>
            <th>是否充电</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="state.videos.length === 0">
            <td colspan="7" class="empty">暂无视频，请添加视频</td>
          </tr>
          <tr v-for="(v, i) in state.videos" :key="v.Bvid + '-' + v.Cid">
            <td>{{ state.offset + i + 1 }}</td>
            <td>{{ formatTitle(v) }}</td>
            <td>{{ v.Author || "-" }}</td>
            <td>{{ formatPubdate(v.Pubdate) }}</td>
            <td class="state" :class="`state-${v.State || 'unknown'}`">{{ stateLabel(v.State) }}</td>
            <td>{{ v.Lang || "-" }}</td>
            <td>{{ v.IsCharging ? "是" : "否" }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="pager">
      <button type="button" :disabled="!state.pager.canPrev" @click="gotoPage(-1)">上一页</button>
      <span class="hint">{{ state.pager.text }}</span>
      <button type="button" :disabled="!state.pager.canNext" @click="gotoPage(1)">下一页</button>
    </div>
  </section>
</template>
