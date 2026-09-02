<script setup lang="ts">
import { computed, nextTick, onUpdated, ref } from "vue";
import { Bot, Box, BrainCircuit, ChevronDown, ChevronRight, Terminal } from "@lucide/vue";
import type { TraceEvent } from "../types";
import { eventLabel, payloadString } from "../types";

const props = defineProps<{
  events: TraceEvent[];
  showPayloads: boolean;
  live: boolean;
}>();

const expanded = ref(new Set<number>());
const autoScroll = ref(true);
const logElement = ref<HTMLElement | null>(null);

const visibleEvents = computed(() => props.events.slice(-2_000));

function toggle(id: number) {
  const next = new Set(expanded.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  expanded.value = next;
}

function iconFor(type: string) {
  if (type.startsWith("tool_")) return Terminal;
  if (type.startsWith("model_") || type.startsWith("turn_") || type === "thinking_level") return BrainCircuit;
  if (type.startsWith("agent_") || type === "session_attached") return Bot;
  return Box;
}

function runLabel(event: TraceEvent): string {
  return String(event.payload.runId ?? "controller");
}

onUpdated(async () => {
  if (!autoScroll.value) return;
  await nextTick();
  logElement.value?.scrollTo({ top: logElement.value.scrollHeight });
});
</script>

<template>
  <section class="session-log-panel">
    <header class="panel-head">
      <div>
        <h2>Session WAL</h2>
        <p>Live redacted events from every agent session</p>
      </div>
      <label class="auto-scroll">
        <input v-model="autoScroll" type="checkbox" />
        follow tail
      </label>
      <span class="source-chip"><i :class="{ live }" />sqlite wal</span>
    </header>

    <div ref="logElement" class="session-log">
      <button
        v-for="event in visibleEvents"
        :key="event.id"
        class="log-event"
        :class="[`event-${event.type}`, { open: expanded.has(event.id) }]"
        @click="toggle(event.id)"
      >
        <span class="expand">
          <ChevronDown v-if="expanded.has(event.id)" :size="15" />
          <ChevronRight v-else :size="15" />
        </span>
        <span class="log-time">{{ new Date(event.created_at).toLocaleTimeString([], { hour12: false }) }}</span>
        <component :is="iconFor(event.type)" class="log-icon" :size="16" />
        <span class="log-type">{{ event.type }}</span>
        <span class="log-run">{{ runLabel(event) }}</span>
        <span class="log-message">{{ eventLabel(event) }}</span>
        <pre v-if="expanded.has(event.id) && showPayloads">{{ payloadString(event.payload) }}</pre>
        <p v-else-if="expanded.has(event.id)" class="payload-hidden">Payload display is disabled in trace options.</p>
      </button>
      <div v-if="visibleEvents.length === 0" class="empty-state">no events match the current trace options</div>
    </div>
  </section>
</template>
