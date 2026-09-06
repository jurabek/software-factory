<script setup lang="ts">
import { computed, ref } from "vue";
import AttemptGraph from "./AttemptGraph.vue";
import type { ArtifactView, Check, Diff, Envelope, Phase } from "../types";

const props = defineProps<{ phases: Phase[]; results: Envelope[]; checks: Check[]; diff: Diff; selectedPhase: string }>();
const emit = defineEmits<{ selectPhase: [id: string]; selectArtifact: [artifact: ArtifactView] }>();
const mode = ref<"graph" | "artifacts">("graph");

const artifacts = computed<ArtifactView[]>(() => {
  const values: ArtifactView[] = props.results.map((result) => ({ id: result.id, kind: "result", title: `${result.agent_role} result`, subtitle: `attempt ${result.attempt}`, content: result.payload }));
  values.push(...props.checks.map((check) => ({ id: `check-${check.id}-${check.duration_ms}`, kind: "check" as const, title: check.name, subtitle: check.status, content: check.output || check.command })));
  values.push(...props.diff.repositories.map((repository) => ({ id: `diff-${repository.repository_id}`, kind: "diff" as const, title: `${repository.name} diff`, subtitle: `${repository.files.length} files`, content: repository.patch || "No changes" })));
  return values;
});
</script>

<template>
  <aside class="artifact-rail" aria-label="Task context">
    <header class="rail-tabs" role="tablist">
      <button :aria-selected="mode === 'graph'" role="tab" type="button" @click="mode = 'graph'">Graph</button>
      <button :aria-selected="mode === 'artifacts'" role="tab" type="button" @click="mode = 'artifacts'">Artifacts <span>{{ artifacts.length }}</span></button>
    </header>
    <AttemptGraph v-if="mode === 'graph'" :phases="phases" :selected-id="selectedPhase" @select="emit('selectPhase', $event)" />
    <div v-else class="artifact-list">
      <button v-for="artifact in artifacts" :key="artifact.id" type="button" @click="emit('selectArtifact', artifact)">
        <span :data-kind="artifact.kind">{{ artifact.kind.slice(0, 1).toUpperCase() }}</span>
        <strong>{{ artifact.title }}</strong>
        <small>{{ artifact.subtitle }}</small>
      </button>
      <p v-if="artifacts.length === 0" class="rail-empty">Artifacts appear as attempts complete.</p>
    </div>
  </aside>
</template>
