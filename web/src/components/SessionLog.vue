<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import type { TraceEvent } from "../types";

const visibleEventLimit = 500;

const props = defineProps<{
  events: TraceEvent[];
  live: boolean;
}>();

const expanded = ref(new Set<string>());
const autoScroll = ref(true);
const showPayloads = ref(false);
const logElement = ref<HTMLElement>();
let scrollFrame = 0;

const visibleEvents = computed(() => props.events.slice(-visibleEventLimit));
const hiddenEventCount = computed(() => Math.max(0, props.events.length - visibleEvents.value.length));

function toggle(id: string) {
  const next = new Set(expanded.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  expanded.value = next;
}

function payloadRecord(event: TraceEvent): Record<string, unknown> {
  return event.payload && typeof event.payload === "object"
    ? event.payload as Record<string, unknown>
    : {};
}

function eventLabel(event: TraceEvent): string {
  const payload = payloadRecord(event);
  for (const key of ["label", "summary", "message", "text", "status", "tool", "command"]) {
    if (typeof payload[key] === "string" && payload[key]) return payload[key];
  }
  return event.name || event.type.replaceAll("_", " ");
}

function phaseLabel(event: TraceEvent): string {
  return event.phase_id || "controller";
}

function payloadString(payload: unknown): string {
  try {
    return JSON.stringify(payload, null, 2);
  } catch {
    return String(payload);
  }
}

watch(
  () => props.events.at(-1)?.sequence,
  async () => {
    if (!autoScroll.value) return;
    await nextTick();
    cancelAnimationFrame(scrollFrame);
    scrollFrame = requestAnimationFrame(() => {
      logElement.value?.scrollTo({ top: logElement.value.scrollHeight });
    });
  },
);

onUnmounted(() => cancelAnimationFrame(scrollFrame));
</script>

<template>
  <section class="session-log-panel">
    <header class="panel-head">
      <div>
        <h2>Session WAL</h2>
        <p>Live events from every factory phase</p>
      </div>
      <label class="auto-scroll">
        <input v-model="autoScroll" type="checkbox" />
        follow tail
      </label>
      <button class="icon-toggle" :class="{ active: showPayloads }" @click="showPayloads = !showPayloads">
        payloads
      </button>
      <span class="source-chip"><i :class="{ live }" />sqlite wal</span>
    </header>

    <p v-if="hiddenEventCount" class="event-window-note">
      {{ hiddenEventCount }} older events hidden to keep this view responsive
    </p>
    <div ref="logElement" class="session-log">
      <button
        v-for="event in visibleEvents"
        :key="event.id"
        class="log-event"
        :class="[`event-${event.type}`, { open: expanded.has(event.id) }]"
        @click="toggle(event.id)"
      >
        <span class="expand">{{ expanded.has(event.id) ? "⌄" : "›" }}</span>
        <time class="log-time" :datetime="event.started_at">
          {{ new Date(event.started_at).toLocaleTimeString([], { hour12: false }) }}
        </time>
        <span class="log-icon">{{ event.type.includes("tool") ? ">_" : "·" }}</span>
        <span class="log-type">{{ event.type }}</span>
        <span class="log-run">{{ phaseLabel(event) }}</span>
        <span class="log-message">{{ eventLabel(event) }}</span>
        <pre v-if="expanded.has(event.id) && showPayloads">{{ payloadString(event.payload) }}</pre>
        <p v-else-if="expanded.has(event.id)" class="payload-hidden">Payload display is disabled.</p>
      </button>
      <div v-if="visibleEvents.length === 0" class="empty-state">waiting for events…</div>
    </div>
  </section>
</template>
