<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { api } from "./api";
import SessionLog from "./components/SessionLog.vue";
import type { Campaign, Check, Diff, Envelope, Health, Phase, TraceEvent } from "./types";

const eventMemoryLimit = 1_000;
const refreshIntervalMilliseconds = 5_000;

const campaigns = ref<Campaign[]>([]);
const selectedId = ref(location.hash.replace(/^#\/?/, ""));
const selected = computed(() => campaigns.value.find((item) => item.id === selectedId.value));
const health = ref<Health>({ status: "loading", errors: [] });
const error = ref("");
const feedback = ref("");
const submittingFeedback = ref(false);
const request = ref("");
const repositoryType = ref<"local" | "github">("local");
const repository = ref("");
const phases = ref<Phase[]>([]);
const events = ref<TraceEvent[]>([]);
const checks = ref<Check[]>([]);
const results = ref<Envelope[]>([]);
const diff = ref<Diff>({ files: [], patch: "" });
const selectedPhase = ref("");
const tracePaused = ref(false);
const traceConnected = ref(false);
const now = ref(Date.now());
const eventSequences = new Set<number>();
let timer = 0;
let stream: EventSource | undefined;

const plan = computed(() => results.value.filter((item) => item.agent_role === "planner" && item.valid).at(-1));
const planData = computed(() => {
  try {
    return plan.value
      ? JSON.parse(plan.value.payload) as { questions?: string[]; steps?: unknown[] }
      : undefined;
  } catch {
    return undefined;
  }
});
const questions = computed(() => planData.value?.questions ?? []);
const active = computed(() => selected.value && ["preparing", "planning", "building", "checking", "reviewing"].includes(selected.value.state));
const inactive = computed(() => selected.value && !active.value);
const filteredEvents = computed(() => selectedPhase.value
  ? events.value.filter((event) => event.phase_id === selectedPhase.value)
  : events.value,
);

const timeline = computed(() => {
  const starts = phases.value.map((phase) => Date.parse(phase.started_at)).filter(Number.isFinite);
  const ends = phases.value
    .map((phase) => phase.ended_at ? Date.parse(phase.ended_at) : now.value)
    .filter(Number.isFinite);
  const start = Math.min(...starts, now.value);
  const end = Math.max(...ends, start + 1_000);
  return { start, span: Math.max(end - start, 1_000) };
});

const lanes = computed(() => {
  const grouped = new Map<string, Phase[]>();
  for (const phase of phases.value) {
    grouped.set(phase.owner, [...(grouped.get(phase.owner) ?? []), phase]);
  }
  return [...grouped.entries()];
});

const campaignDuration = computed(() => {
  if (!selected.value) return "—";
  const start = Date.parse(selected.value.started_at || selected.value.created_at);
  const end = selected.value.ended_at ? Date.parse(selected.value.ended_at) : now.value;
  return formatDuration(end - start);
});

async function refreshOverview() {
  try {
    const [campaignRows, currentHealth] = await Promise.all([api.campaigns(), api.health()]);
    campaigns.value = campaignRows ?? [];
    health.value = { ...currentHealth, errors: currentHealth.errors ?? [] };
    error.value = "";
  } catch (cause) {
    error.value = errorMessage(cause);
  }
}

async function refreshDetail() {
  const id = selectedId.value;
  if (!id) return;
  try {
    const [phaseRows, checkRows, envelopes, changed] = await Promise.all([
      api.phases(id),
      api.checks(id),
      api.results(id),
      api.diff(id).catch(() => ({ files: [], patch: "" })),
    ]);
    if (id !== selectedId.value) return;
    phases.value = phaseRows ?? [];
    checks.value = checkRows ?? [];
    results.value = envelopes ?? [];
    diff.value = { files: changed.files ?? [], patch: changed.patch ?? "" };
    now.value = Date.now();
  } catch (cause) {
    error.value = errorMessage(cause);
  }
}

async function refresh() {
  await Promise.all([refreshOverview(), refreshDetail()]);
}

async function loadEventTail(id: string) {
  try {
    const page = await api.events(id, { tail: eventMemoryLimit });
    if (id !== selectedId.value) return;
    events.value = page.events ?? [];
    eventSequences.clear();
    for (const event of events.value) eventSequences.add(event.sequence);
  } catch (cause) {
    error.value = errorMessage(cause);
  }
}

async function create() {
  const source = repositoryType.value === "local"
    ? { type: "local", path: repository.value }
    : { type: "github", repo: repository.value };
  const campaign = await api.create({ request: request.value, repository: source });
  request.value = "";
  repository.value = "";
  await refreshOverview();
  open(campaign);
}

async function command(name: string) {
  if (!selected.value) return;
  await api.command(selected.value.id, name);
  await refresh();
}

async function submitFeedback() {
  if (!selected.value || !feedback.value.trim() || !plan.value) return;
  submittingFeedback.value = true;
  try {
    await api.feedback(selected.value.id, feedback.value, selected.value.plan_digest);
    feedback.value = "";
    await refresh();
  } catch (cause) {
    error.value = errorMessage(cause);
  } finally {
    submittingFeedback.value = false;
  }
}

async function remove() {
  if (!selected.value) return;
  await api.remove(selected.value.id);
  location.hash = "#/";
}

function open(campaign: Campaign) {
  location.hash = `#/${campaign.id}`;
}

function back() {
  location.hash = "#/";
}

function route() {
  selectedId.value = location.hash.replace(/^#\/?/, "");
}

async function loadSelection(id: string) {
  stream?.close();
  stream = undefined;
  traceConnected.value = false;
  phases.value = [];
  events.value = [];
  checks.value = [];
  results.value = [];
  diff.value = { files: [], patch: "" };
  selectedPhase.value = "";
  eventSequences.clear();
  if (!id) return;
  await Promise.all([refreshDetail(), loadEventTail(id)]);
  if (!tracePaused.value) connectStream(id);
}

async function resumeTrace() {
  const id = selectedId.value;
  if (!id) return;
  await loadEventTail(id);
  if (!tracePaused.value && id === selectedId.value) connectStream(id);
}

function connectStream(id: string) {
  const cursor = events.value.at(-1)?.sequence ?? 0;
  stream = new EventSource(`/api/v1/campaigns/${encodeURIComponent(id)}/events/stream?after=${cursor}`);
  stream.onopen = () => {
    if (id !== selectedId.value) return;
    traceConnected.value = true;
    error.value = "";
  };
  stream.addEventListener("event", (message) => {
    if (tracePaused.value || id !== selectedId.value) return;
    try {
      appendEvent(JSON.parse((message as MessageEvent<string>).data) as TraceEvent);
    } catch (cause) {
      error.value = `Invalid live event: ${errorMessage(cause)}`;
    }
  });
  stream.onerror = () => {
    if (id !== selectedId.value) return;
    traceConnected.value = false;
    error.value = "Live trace reconnecting";
  };
}

function appendEvent(event: TraceEvent) {
  if (eventSequences.has(event.sequence)) return;
  eventSequences.add(event.sequence);
  events.value.push(event);
  const overflow = events.value.length - eventMemoryLimit;
  if (overflow <= 0) return;
  for (const removed of events.value.splice(0, overflow)) eventSequences.delete(removed.sequence);
}

function phaseStyle(phase: Phase): Record<string, string> {
  const start = Date.parse(phase.started_at);
  if (!Number.isFinite(start)) return { left: "90%", width: "9%" };
  const end = phase.ended_at ? Date.parse(phase.ended_at) : now.value;
  const rawLeft = ((start - timeline.value.start) / timeline.value.span) * 100;
  const width = Math.min(Math.max(((end - start) / timeline.value.span) * 100, 8), 99);
  const left = Math.min(Math.max(rawLeft, 0), 99 - width);
  return { left: `${left}%`, width: `${width}%` };
}

function phaseDuration(phase: Phase): string {
  const start = Date.parse(phase.started_at);
  const end = phase.ended_at ? Date.parse(phase.ended_at) : now.value;
  return formatDuration(end - start);
}

function toolTicks(phase: Phase): number[] {
  const start = Date.parse(phase.started_at);
  const end = phase.ended_at ? Date.parse(phase.ended_at) : now.value;
  const span = Math.max(end - start, 1);
  return events.value
    .filter((event) => event.phase_id === phase.id && (event.type === "tool_call" || event.type.startsWith("tool_")))
    .map((event) => Math.min(Math.max(((Date.parse(event.started_at) - start) / span) * 100, 2), 98));
}

function formatDuration(milliseconds: number): string {
  if (!Number.isFinite(milliseconds)) return "—";
  if (milliseconds < 1_000) return `${Math.max(0, Math.round(milliseconds))}ms`;
  if (milliseconds < 60_000) return `${(milliseconds / 1_000).toFixed(1)}s`;
  return `${Math.floor(milliseconds / 60_000)}m ${Math.round((milliseconds % 60_000) / 1_000)}s`;
}

function errorMessage(cause: unknown): string {
  return cause instanceof Error ? cause.message : String(cause);
}

watch(selectedId, (id) => void loadSelection(id));
watch(tracePaused, (paused) => {
  if (paused) {
    stream?.close();
    stream = undefined;
    traceConnected.value = false;
    return;
  }
  void resumeTrace();
});

onMounted(async () => {
  window.addEventListener("hashchange", route);
  await api.initialize();
  await refreshOverview();
  await loadSelection(selectedId.value);
  timer = window.setInterval(() => void refresh(), refreshIntervalMilliseconds);
});

onUnmounted(() => {
  window.removeEventListener("hashchange", route);
  clearInterval(timer);
  stream?.close();
});
</script>

<template>
  <header class="topbar">
    <button class="brand" @click="back">
      <span class="logo">SF</span><span>Software Factory</span>
      <template v-if="selected"><b>›</b><span class="crumb active">{{ selected.id }}</span></template>
    </button>
    <span class="live-indicator" :class="{ offline: health.status !== 'ok' }"><i />{{ health.status }}</span>
  </header>
  <div v-if="health.errors.length" class="error-bar">{{ health.errors.join(" · ") }}</div>
  <div v-if="error" class="error-bar">{{ error }}</div>

  <main v-if="!selected" class="campaign-page">
    <section class="page-heading">
      <div><p class="kicker">ONE REPOSITORY · ONE ACTIVE CAMPAIGN</p><h1>Campaigns</h1></div>
      <span class="count">{{ campaigns.length }} total</span>
    </section>
    <form class="campaign-card create-card" @submit.prevent="create">
      <div class="card-top"><span class="campaign-id">NEW CAMPAIGN</span></div>
      <textarea v-model="request" required placeholder="Describe the repository outcome" />
      <div class="source-row">
        <select v-model="repositoryType"><option value="local">Local repository</option><option value="github">GitHub repository</option></select>
        <input v-model="repository" required :placeholder="repositoryType === 'local' ? '/absolute/path' : 'owner/repository'" />
      </div>
      <button class="primary" :disabled="health.status !== 'ok'">Create draft</button>
    </form>
    <section class="campaign-grid">
      <button
        v-for="campaign in campaigns"
        :key="campaign.id"
        class="campaign-card"
        :class="{ running: !['draft','completed','blocked','paused','aborted'].includes(campaign.state), failed: ['blocked','aborted'].includes(campaign.state) }"
        @click="open(campaign)"
      >
        <div class="card-top"><span class="campaign-id">{{ campaign.id }}</span><span class="status-chip" :data-status="campaign.state"><i />{{ campaign.state }}</span></div>
        <h2>{{ campaign.request }}</h2>
        <p class="profile">{{ campaign.repository_type }} · {{ campaign.repository_value }}</p>
        <div class="mini-trace"><span v-for="name in ['plan','build','check','review']" :key="name" :class="{ done: campaign.state === 'completed' }"><i /><small>{{ name }}</small></span></div>
        <div class="card-footer"><span>{{ new Date(campaign.created_at).toLocaleString() }}</span><span>{{ campaign.base_sha?.slice(0, 8) || 'not pinned' }}</span></div>
      </button>
    </section>
  </main>

  <main v-else class="trace-page">
    <section class="run-strip">
      <button class="back-button" @click="back">←</button>
      <div class="run-title"><span>{{ selected.id }}</span><strong>{{ selected.request }}</strong></div>
      <span class="status-chip" :data-status="selected.state"><i />{{ selected.state }}</span>
      <span class="stat">{{ campaignDuration }}</span>
      <span class="stat">{{ checks.filter((item) => item.status === 'passed').length }}/{{ checks.length }} checks</span>
      <span class="stat">{{ selected.repository_value }}</span>
    </section>
    <nav class="trace-controls">
      <button v-if="selected.state === 'draft'" class="primary" @click="command('start')">Start</button>
      <button v-if="selected.state === 'awaiting_plan_approval'" class="primary" :disabled="questions.length > 0" @click="command('approve')">Approve plan</button>
      <button v-if="active" @click="command('pause')">Pause campaign</button>
      <button v-if="selected.state === 'paused' || selected.state === 'blocked'" @click="command('resume')">Resume campaign</button>
      <button v-if="!['completed','aborted'].includes(selected.state)" @click="command('abort')">Abort</button>
      <button v-if="inactive" @click="remove">Delete</button>
      <span class="controls-spacer" />
      <button class="icon-toggle" :class="{ active: tracePaused }" @click="tracePaused = !tracePaused">{{ tracePaused ? 'Resume trace' : 'Pause trace' }}</button>
    </nav>

    <section class="waterfall">
      <div class="axis-row">
        <span>agent sessions</span>
        <div><i v-for="tick in 4" :key="tick" :style="{ left: `${tick * 20}%` }">{{ formatDuration(timeline.span * tick / 5) }}</i></div>
      </div>
      <div v-for="[lane, lanePhases] in lanes" :key="lane" class="lane-row">
        <label>{{ lane }}</label>
        <div class="lane-track">
          <span v-for="tick in 4" :key="tick" class="gridline" :style="{ left: `${tick * 20}%` }" />
          <button
            v-for="phase in lanePhases"
            :key="phase.id"
            class="phase-block"
            :class="[phase.status, { selected: selectedPhase === phase.id }]"
            :style="phaseStyle(phase)"
            @click="selectedPhase = selectedPhase === phase.id ? '' : phase.id"
          >
            <span><b>{{ phase.status === 'running' ? '●' : ['success', 'completed', 'passed'].includes(phase.status) ? '✓' : '✗' }}</b>{{ phase.name }}</span>
            <small>{{ phaseDuration(phase) }}</small>
            <i v-for="position in toolTicks(phase)" :key="position" class="tool-tick" :style="{ left: `${position}%` }" />
          </button>
        </div>
      </div>
      <div v-if="lanes.length === 0" class="empty-state">waiting for agent phases…</div>
    </section>

    <SessionLog :events="filteredEvents" :live="traceConnected && !tracePaused" />

    <section class="detail-grid supporting-detail">
      <article class="panel">
        <p class="kicker">REPOSITORY</p>
        <dl><dt>Workspace</dt><dd>{{ selected.workspace_path || 'created on start' }}</dd><dt>Base SHA</dt><dd>{{ selected.base_sha || '—' }}</dd><dt>Error</dt><dd>{{ selected.error || '—' }}</dd></dl>
      </article>
      <article v-if="plan" class="panel">
        <p class="kicker">GENERATED PLAN</p>
        <div v-if="questions.length" class="questions"><strong>UNRESOLVED QUESTIONS</strong><ul><li v-for="question in questions" :key="question">{{ question }}</li></ul></div>
        <pre>{{ JSON.stringify(planData, null, 2) }}</pre>
        <form v-if="selected.state === 'awaiting_plan_approval' && questions.length" class="feedback-form" @submit.prevent="submitFeedback">
          <textarea v-model="feedback" required placeholder="Answer the unresolved questions" />
          <button class="primary" :disabled="submittingFeedback || !feedback.trim()">{{ submittingFeedback ? 'Submitting…' : 'Send feedback' }}</button>
        </form>
      </article>
      <article class="panel"><p class="kicker">CHECKS</p><div v-for="check in checks" :key="check.id" class="event-row"><strong>{{ check.name }}</strong><span class="status-chip">{{ check.status }}</span><code>{{ check.command }}</code></div></article>
      <article class="panel"><p class="kicker">CHANGED FILES</p><code v-for="file in diff.files" :key="file">{{ file }}</code><pre>{{ diff.patch }}</pre></article>
    </section>
  </main>
</template>
