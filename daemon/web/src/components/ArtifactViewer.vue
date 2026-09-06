<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { ArtifactView } from "../types";

const props = defineProps<{ artifact: ArtifactView }>();
defineEmits<{ close: []; comment: [quote: string] }>();
const mode = ref<"rendered" | "raw">("rendered");
const selection = ref("");

const rendered = computed(() => {
  if (props.artifact.kind === "result") {
    try { return JSON.stringify(JSON.parse(props.artifact.content), null, 2); } catch { return props.artifact.content; }
  }
  return props.artifact.content;
});

watch(() => props.artifact.id, () => { mode.value = "rendered"; selection.value = ""; });

function captureSelection() {
  selection.value = window.getSelection()?.toString().trim() ?? "";
}
</script>

<template>
  <article class="artifact-view">
    <header class="artifact-view__header">
      <div><p>{{ artifact.kind }}</p><h2>{{ artifact.title }}</h2><small>{{ artifact.subtitle }}</small></div>
      <nav><button :aria-pressed="mode === 'rendered'" type="button" @click="mode = 'rendered'">Rendered</button><button :aria-pressed="mode === 'raw'" type="button" @click="mode = 'raw'">Raw</button><button type="button" @click="$emit('close')">×</button></nav>
    </header>
    <pre class="artifact-source" @mouseup="captureSelection">{{ mode === 'rendered' ? rendered : artifact.content }}</pre>
    <footer v-if="selection" class="selection-action"><span>“{{ selection.slice(0, 72) }}{{ selection.length > 72 ? '…' : '' }}”</span><button class="button button--accent" type="button" @click="$emit('comment', selection)">Comment</button></footer>
  </article>
</template>
