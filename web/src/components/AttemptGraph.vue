<script setup lang="ts">
import type { Phase } from "../types";

defineProps<{ phases: Phase[]; selectedId: string }>();
defineEmits<{ select: [id: string] }>();
</script>

<template>
  <div class="attempt-graph" aria-label="Task attempt graph">
    <button
      v-for="phase in phases"
      :key="phase.id"
      class="attempt-node"
      :class="{ selected: selectedId === phase.id }"
      :data-state="phase.status"
      type="button"
      @click="$emit('select', phase.id)"
    >
      <span>{{ phase.name }}</span>
      <small>#{{ phase.attempt || 1 }} · {{ phase.status }}</small>
    </button>
    <p v-if="phases.length === 0" class="rail-empty">Attempts appear here when the Task starts.</p>
  </div>
</template>
