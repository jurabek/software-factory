<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import type { TraceEvent } from "../types";

const visibleEventLimit = 500;
const previewLimit = 180;
const outputKeys = ["result", "output", "text", "message", "error"];
const transientEventTypes = new Set(["message_start", "message_update", "tool_execution_update"]);

const props = defineProps<{
  events: TraceEvent[];
  live: boolean;
}>();

const autoScroll = ref(true);
const logElement = ref<HTMLElement>();
const detailDialog = ref<HTMLElement>();
const selectedEvent = ref<TraceEvent>();
let detailTrigger: HTMLElement | undefined;
let scrollFrame = 0;

const meaningfulEvents = computed(() => props.events.filter((event) => !transientEventTypes.has(event.type)));
const visibleEvents = computed(() => meaningfulEvents.value.slice(-visibleEventLimit));
const presentedEvents = computed(() => visibleEvents.value.map((event) => ({
  event,
  duration: durationLabel(event),
  icon: eventIcon(event),
  preview: eventPreview(event),
  success: selectedSuccess(event),
  target: eventTarget(event),
  title: eventTitle(event),
})));
const hiddenEventCount = computed(() => Math.max(0, meaningfulEvents.value.length - visibleEvents.value.length));
const selectedPayload = computed(() => selectedEvent.value ? payloadRecord(selectedEvent.value) : {});
const selectedArgumentEntries = computed(() => selectedEvent.value ? eventArgumentEntries(selectedEvent.value) : []);
const selectedDetailEntries = computed(() => Object.entries(selectedPayload.value).filter(([key]) =>
  key !== "arguments" && key !== "args" && !outputKeys.includes(key),
));
const selectedResult = computed(() => selectedEvent.value ? eventResult(selectedEvent.value) : "");

function payloadRecord(event: TraceEvent): Record<string, unknown> {
  return event.payload && typeof event.payload === "object" && !Array.isArray(event.payload)
    ? event.payload as Record<string, unknown>
    : {};
}

function parseRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) return value as Record<string, unknown>;
  if (typeof value !== "string") return {};
  try {
    const parsed: unknown = JSON.parse(value);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : {};
  } catch {
    return {};
  }
}

function eventArguments(event: TraceEvent): Record<string, unknown> {
  const payload = payloadRecord(event);
  return parseRecord(payload.arguments ?? payload.args);
}

function eventArgumentEntries(event: TraceEvent): [string, unknown][] {
  const payload = payloadRecord(event);
  const rawArguments = payload.arguments ?? payload.args;
  const parsedArguments = parseRecord(rawArguments);
  const entries = Object.entries(parsedArguments);
  if (entries.length > 0 || rawArguments === undefined || rawArguments === null || rawArguments === "") return entries;
  return [["arguments", rawArguments]];
}

function eventResult(event: TraceEvent): string {
  const payload = payloadRecord(event);
  for (const key of outputKeys) {
    const value = payload[key];
    if (typeof value === "string") {
      if (value.trim()) return value;
      continue;
    }
    if (key === "message") {
      const text = messageText(value);
      if (text) return text;
    }
    if (value !== undefined && value !== null) return formatValue(value);
  }
  return "";
}

function messageText(value: unknown): string {
  const message = parseRecord(value);
  if (typeof message.content === "string") return message.content;
  if (!Array.isArray(message.content)) return "";
  return message.content
    .map((part) => parseRecord(part).text)
    .filter((text): text is string => typeof text === "string")
    .join("\n");
}

function toolName(event: TraceEvent): string {
  const payload = payloadRecord(event);
  const raw = String(payload.tool || event.name || "Tool");
  const knownNames: Record<string, string> = {
    apply_patch: "Edit",
    bash: "Bash",
    edit: "Edit",
    glob: "Files",
    grep: "Search",
    read: "Read",
    web_fetch: "Web Fetch",
    webfetch: "Web Fetch",
    write: "Write",
  };
  return knownNames[raw.toLowerCase()] || raw.replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function eventTitle(event: TraceEvent): string {
  if (event.type === "tool_call") return toolName(event);
  const titles: Record<string, string> = {
    message_end: "Agent response",
    phase_end: "Attempt finished",
    phase_start: "Attempt started",
    process_end: "Agent process finished",
    process_start: "Agent process started",
  };
  return titles[event.type] || event.name || event.type.replaceAll("_", " ");
}

function eventTarget(event: TraceEvent): string {
  const payload = payloadRecord(event);
  const argumentsRecord = eventArguments(event);
  for (const key of ["file_path", "path", "url", "command", "pattern", "query"]) {
    const value = argumentsRecord[key] ?? payload[key];
    if (typeof value === "string" && value.trim()) return value;
  }
  if (typeof payload.label === "string") return payload.label;
  if (event.type === "phase_start" || event.type === "phase_end") return event.name || "";
  return "";
}

function eventPreview(event: TraceEvent): string {
  const result = eventResult(event).trim();
  if (!result) return "";
  const firstLine = result.split("\n").find((line) => line.trim())?.trim() || "";
  return firstLine.length > previewLimit ? `${firstLine.slice(0, previewLimit)}...` : firstLine;
}

function eventIcon(event: TraceEvent): string {
  if (event.type === "tool_call") return toolName(event).slice(0, 1).toUpperCase();
  if (event.type.includes("error") || eventResult(event) && selectedSuccess(event) === false) return "!";
  if (event.type.includes("end")) return "+";
  return ">";
}

function selectedSuccess(event: TraceEvent): boolean | undefined {
  const payload = payloadRecord(event);
  if (typeof payload.success === "boolean") return payload.success;
  if (typeof payload.exit_code === "number") return payload.exit_code === 0;
  if (typeof payload.status === "string") {
    if (["failed", "error", "aborted"].includes(payload.status)) return false;
    if (["passed", "completed", "success"].includes(payload.status)) return true;
  }
  if (event.type.includes("error")) return false;
  return undefined;
}

function durationLabel(event: TraceEvent): string {
  const duration = payloadRecord(event).duration_ms;
  if (typeof duration !== "number") return "";
  if (duration < 1_000) return `${duration}ms`;
  if (duration < 60_000) return `${(duration / 1_000).toFixed(duration < 10_000 ? 1 : 0)}s`;
  const totalSeconds = Math.round(duration / 1_000);
  return `${Math.floor(totalSeconds / 60)}m ${totalSeconds % 60}s`;
}

function startedAt(event: TraceEvent): Date {
  const payloadStartedAt = payloadRecord(event).started_at;
  return new Date(typeof payloadStartedAt === "string" ? payloadStartedAt : event.started_at);
}

function formatValue(value: unknown): string {
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

async function openDetail(event: TraceEvent, clickEvent: MouseEvent) {
  detailTrigger = clickEvent.currentTarget instanceof HTMLElement ? clickEvent.currentTarget : undefined;
  selectedEvent.value = event;
  await nextTick();
  detailDialog.value?.focus();
}

async function closeDetail() {
  selectedEvent.value = undefined;
  await nextTick();
  detailTrigger?.focus();
  detailTrigger = undefined;
}

function handleDialogKeydown(event: KeyboardEvent) {
  if (event.key === "Escape") {
    event.preventDefault();
    void closeDetail();
    return;
  }
  if (event.key !== "Tab" || !detailDialog.value) return;
  const focusable = [...detailDialog.value.querySelectorAll<HTMLElement>("button, summary, [href], input, select, textarea, [tabindex]:not([tabindex='-1'])")]
    .filter((element) => !element.hasAttribute("disabled"));
  if (focusable.length === 0) {
    event.preventDefault();
    detailDialog.value.focus();
    return;
  }
  const first = focusable[0];
  const last = focusable.at(-1);
  if (event.shiftKey && (document.activeElement === detailDialog.value || document.activeElement === first)) {
    event.preventDefault();
    last?.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
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
        <h2>Work log</h2>
        <p>Actions and results from every task attempt</p>
      </div>
      <label class="auto-scroll">
        <input v-model="autoScroll" type="checkbox" />
        follow tail
      </label>
      <span class="source-chip"><i :class="{ live }" />{{ live ? "live" : "reconnecting" }}</span>
    </header>

    <p v-if="hiddenEventCount" class="event-window-note">
      {{ hiddenEventCount }} older events hidden to keep this view responsive
    </p>
    <div ref="logElement" class="session-log">
      <button
        v-for="item in presentedEvents"
        :key="item.event.id"
        class="log-event"
        :class="`event-${item.event.type}`"
        :data-success="item.success"
        type="button"
        aria-haspopup="dialog"
        @click="openDetail(item.event, $event)"
      >
        <span class="log-track" aria-hidden="true"><i>{{ item.icon }}</i></span>
        <span class="log-content">
          <span class="log-heading">
            <strong>{{ item.title }}</strong>
            <span v-if="item.target" class="log-target">{{ item.target }}</span>
          </span>
          <span v-if="item.preview" class="log-preview">{{ item.preview }}</span>
        </span>
        <span class="log-meta">
          <span v-if="item.duration">{{ item.duration }}</span>
          <time :datetime="item.event.started_at">{{ new Date(item.event.started_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }) }}</time>
          <span class="log-open" aria-hidden="true">open</span>
        </span>
      </button>
      <div v-if="presentedEvents.length === 0" class="empty-state">waiting for events...</div>
    </div>
  </section>

  <Teleport to="body">
    <div v-if="selectedEvent" class="event-dialog-backdrop" @mousedown.self="closeDetail">
      <section
        ref="detailDialog"
        class="event-dialog"
        role="dialog"
        aria-modal="true"
        :aria-label="`${eventTitle(selectedEvent)} event details`"
        tabindex="-1"
        @keydown="handleDialogKeydown"
      >
        <header class="event-dialog__head">
          <span class="event-dialog__icon" :data-success="selectedSuccess(selectedEvent)">{{ eventIcon(selectedEvent) }}</span>
          <div>
            <h2>{{ eventTitle(selectedEvent) }}</h2>
            <p v-if="eventTarget(selectedEvent)">{{ eventTarget(selectedEvent) }}</p>
          </div>
          <button type="button" aria-label="Close event details" @click="closeDetail">x</button>
        </header>

        <div class="event-dialog__body">
          <dl class="event-summary">
            <div><dt>Status</dt><dd :data-success="selectedSuccess(selectedEvent)">{{ selectedSuccess(selectedEvent) === false ? "failed" : selectedSuccess(selectedEvent) ? "completed" : "recorded" }}</dd></div>
            <div><dt>Started</dt><dd>{{ startedAt(selectedEvent).toLocaleString() }}</dd></div>
            <div><dt>Duration</dt><dd>{{ durationLabel(selectedEvent) || "not reported" }}</dd></div>
            <div><dt>Attempt</dt><dd>{{ selectedEvent.phase_id || "controller" }}</dd></div>
          </dl>

          <section v-if="selectedArgumentEntries.length" class="event-detail-section">
            <h3>Input</h3>
            <dl class="event-fields">
              <div v-for="([key, value]) in selectedArgumentEntries" :key="key">
                <dt>{{ key.replaceAll("_", " ") }}</dt>
                <dd><pre>{{ formatValue(value) }}</pre></dd>
              </div>
            </dl>
          </section>

          <section v-if="selectedResult" class="event-detail-section event-result">
            <h3>Result</h3>
            <pre>{{ selectedResult }}</pre>
          </section>

          <section v-if="!selectedResult && selectedDetailEntries.length" class="event-detail-section">
            <h3>Details</h3>
            <dl class="event-fields">
              <div v-for="([key, value]) in selectedDetailEntries" :key="key">
                <dt>{{ key.replaceAll("_", " ") }}</dt>
                <dd><pre>{{ formatValue(value) }}</pre></dd>
              </div>
            </dl>
          </section>

          <details class="raw-event">
            <summary>Raw event payload</summary>
            <pre>{{ formatValue(selectedEvent.payload) }}</pre>
          </details>
        </div>

        <footer class="event-dialog__foot">
          <span>{{ selectedEvent.type }}</span>
          <span>event {{ selectedEvent.sequence }}</span>
          <span>Esc to close</span>
        </footer>
      </section>
    </div>
  </Teleport>
</template>
