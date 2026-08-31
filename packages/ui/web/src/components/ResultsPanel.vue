<script setup lang="ts">
import { computed } from "vue";
import { FileCheck2 } from "@lucide/vue";
import { summarizeAgentResult } from "@software-factory/core/result-summary";
import type { AgentResult } from "@software-factory/core";

const props = defineProps<{ results: AgentResult[] }>();
const summaries = computed(() => props.results.map(summarizeAgentResult));
</script>

<template>
  <section v-if="summaries.length" class="results-panel">
    <header class="panel-head">
      <FileCheck2 :size="18" />
      <div>
        <h2>Agent results</h2>
        <p>Human-readable handoffs from completed agent sessions</p>
      </div>
      <span class="source-chip">{{ summaries.length }} results</span>
    </header>
    <div class="result-grid">
      <details v-for="result in summaries" :key="`${result.role}-${result.completedAt}-${result.workItemId}`" open>
        <summary>
          <span>
            <b>{{ result.role }}</b>
            <small v-if="result.workItemId">{{ result.workItemId }}</small>
          </span>
          <span class="status-chip" :data-status="result.status"><i />{{ result.status.replaceAll("_", " ") }}</span>
        </summary>
        <div class="result-body">
          <p>{{ result.summary }}</p>
          <section v-for="section in result.sections" :key="section.title">
            <h3>{{ section.title }}</h3>
            <ul>
              <li v-for="item in section.items" :key="item">{{ item }}</li>
            </ul>
          </section>
          <time :datetime="result.completedAt">Completed {{ new Date(result.completedAt).toLocaleString() }}</time>
        </div>
      </details>
    </div>
  </section>
</template>
