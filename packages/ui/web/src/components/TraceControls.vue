<script setup lang="ts">
import { Eye, EyeOff, Filter, Pause, Play } from "@lucide/vue";
import { traceTypeGroups } from "../types";

const props = defineProps<{
  selectedGroups: string[];
  role: string;
  runId: string;
  roles: string[];
  runs: Array<{ id: string; label: string }>;
  paused: boolean;
  showPayloads: boolean;
}>();

const emit = defineEmits<{
  "update:selectedGroups": [value: string[]];
  "update:role": [value: string];
  "update:runId": [value: string];
  "update:paused": [value: boolean];
  "update:showPayloads": [value: boolean];
}>();

function toggleGroup(group: string) {
  const next = props.selectedGroups.includes(group)
    ? props.selectedGroups.filter((item) => item !== group)
    : [...props.selectedGroups, group];
  emit("update:selectedGroups", next);
}
</script>

<template>
  <div class="trace-controls">
    <span class="control-title"><Filter :size="17" /> trace</span>
    <button
      v-for="(_, group) in traceTypeGroups"
      :key="group"
      class="toggle"
      :class="{ active: selectedGroups.includes(group) }"
      @click="toggleGroup(group)"
    >
      {{ group }}
    </button>
    <select :value="role" aria-label="Filter by role" @change="$emit('update:role', ($event.target as HTMLSelectElement).value)">
      <option value="">all roles</option>
      <option v-for="item in roles" :key="item" :value="item">{{ item }}</option>
    </select>
    <select :value="runId" aria-label="Filter by session" @change="$emit('update:runId', ($event.target as HTMLSelectElement).value)">
      <option value="">all sessions</option>
      <option v-for="run in runs" :key="run.id" :value="run.id">{{ run.label }}</option>
    </select>
    <span class="controls-spacer" />
    <button class="icon-toggle" :title="showPayloads ? 'Hide event payloads' : 'Show event payloads'" @click="$emit('update:showPayloads', !showPayloads)">
      <Eye v-if="showPayloads" :size="17" />
      <EyeOff v-else :size="17" />
      payloads
    </button>
    <button class="icon-toggle" :class="{ active: paused }" @click="$emit('update:paused', !paused)">
      <Play v-if="paused" :size="17" />
      <Pause v-else :size="17" />
      {{ paused ? "resume" : "pause" }}
    </button>
  </div>
</template>
