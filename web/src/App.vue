<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { api } from "./api";
import type { Campaign, Check, Diff, Envelope, Health, Phase, TraceEvent } from "./types";

const campaigns = ref<Campaign[]>([]);
const selectedId = ref(location.hash.replace(/^#\/?/, ""));
const selected = computed(() => campaigns.value.find((item) => item.id === selectedId.value));
const health = ref<Health>({ status: "loading", errors: [] });
const error = ref("");
const request = ref("");
const repositoryType = ref<"local" | "github">("local");
const repository = ref("");
const phases = ref<Phase[]>([]);
const events = ref<TraceEvent[]>([]);
const checks = ref<Check[]>([]);
const results = ref<Envelope[]>([]);
const diff = ref<Diff>({ files: [], patch: "" });
let timer = 0;
let stream: EventSource | undefined;

async function refresh() {
  try {
    campaigns.value = (await api.campaigns()) ?? [];
    const currentHealth = await api.health();
    health.value = { ...currentHealth, errors: currentHealth.errors ?? [] };
    if (selectedId.value) await refreshDetail();
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : String(cause); }
}
async function refreshDetail() {
  const id = selectedId.value;
  if (!id) return;
  const [phaseRows, eventPage, checkRows, envelopes, changed] = await Promise.all([
    api.phases(id), api.events(id), api.checks(id), api.results(id), api.diff(id).catch(() => ({ files: [], patch: "" })),
  ]);
  phases.value = phaseRows ?? []; events.value = eventPage.events ?? []; checks.value = checkRows ?? []; results.value = envelopes ?? []; diff.value = { files: changed.files ?? [], patch: changed.patch ?? "" };
}
async function create() {
  const source = repositoryType.value === "local" ? { type: "local", path: repository.value } : { type: "github", repo: repository.value };
  const campaign = await api.create({ request: request.value, repository: source });
  request.value = ""; repository.value = ""; await refresh(); open(campaign);
}
async function command(name: string) { if (!selected.value) return; await api.command(selected.value.id, name); await refresh(); }
async function remove() { if (!selected.value) return; await api.remove(selected.value.id); location.hash = "#/"; }
function open(campaign: Campaign) { location.hash = `#/${campaign.id}`; }
function back() { location.hash = "#/"; }
function route() { selectedId.value = location.hash.replace(/^#\/?/, ""); }
function connectStream() {
  stream?.close(); stream = undefined;
  if (!selectedId.value) return;
  const cursor = events.value.at(-1)?.sequence ?? 0;
  stream = new EventSource(`/api/v1/campaigns/${encodeURIComponent(selectedId.value)}/events/stream?after=${cursor}`);
  stream.addEventListener("event", (message) => {
    const event = JSON.parse((message as MessageEvent<string>).data) as TraceEvent;
    if (!events.value.some((item) => item.sequence === event.sequence)) events.value.push(event);
    void refresh();
  });
  stream.onerror = () => { error.value = "Live trace reconnecting"; };
}
const plan = computed(() => results.value.filter((item) => item.agent_role === "planner" && item.valid).at(-1));
const active = computed(() => selected.value && ["preparing", "planning", "building", "checking", "reviewing"].includes(selected.value.state));
const inactive = computed(() => selected.value && !active.value);
watch(selectedId, async () => { await refreshDetail(); connectStream(); });
onMounted(async () => { window.addEventListener("hashchange", route); await api.initialize(); await refresh(); connectStream(); timer = window.setInterval(refresh, 5000); });
onUnmounted(() => { window.removeEventListener("hashchange", route); clearInterval(timer); stream?.close(); });
</script>

<template>
  <header class="topbar">
    <button class="brand" @click="back"><span class="logo">SF</span><span>Software Factory</span><template v-if="selected"><b>›</b><span class="crumb active">{{ selected.id }}</span></template></button>
    <span class="live-indicator" :class="{ offline: health.status !== 'ok' }"><i />{{ health.status }}</span>
  </header>
  <div v-if="health.errors.length" class="error-bar">{{ health.errors.join(" · ") }}</div>
  <div v-if="error" class="error-bar">{{ error }}</div>

  <main v-if="!selected" class="campaign-page">
    <section class="page-heading"><div><p class="kicker">ONE REPOSITORY · ONE ACTIVE CAMPAIGN</p><h1>Campaigns</h1></div><span class="count">{{ campaigns.length }} total</span></section>
    <form class="campaign-card create-card" @submit.prevent="create">
      <div class="card-top"><span class="campaign-id">NEW CAMPAIGN</span></div>
      <textarea v-model="request" required placeholder="Describe the repository outcome" />
      <div class="source-row"><select v-model="repositoryType"><option value="local">Local repository</option><option value="github">GitHub repository</option></select><input v-model="repository" required :placeholder="repositoryType === 'local' ? '/absolute/path' : 'owner/repository'" /></div>
      <button class="primary" :disabled="health.status !== 'ok'">Create draft</button>
    </form>
    <section class="campaign-grid">
      <button v-for="campaign in campaigns" :key="campaign.id" class="campaign-card" :class="{ running: !['draft','completed','blocked','paused','aborted'].includes(campaign.state), failed: ['blocked','aborted'].includes(campaign.state) }" @click="open(campaign)">
        <div class="card-top"><span class="campaign-id">{{ campaign.id }}</span><span class="status-chip" :data-status="campaign.state"><i />{{ campaign.state }}</span></div>
        <h2>{{ campaign.request }}</h2><p class="profile">{{ campaign.repository_type }} · {{ campaign.repository_value }}</p>
        <div class="mini-trace"><span v-for="name in ['plan','build','check','review']" :key="name" :class="{ done: campaign.state === 'completed' }"><i /><small>{{ name }}</small></span></div>
        <div class="card-footer"><span>{{ new Date(campaign.created_at).toLocaleString() }}</span><span>{{ campaign.base_sha?.slice(0, 8) || 'not pinned' }}</span></div>
      </button>
    </section>
  </main>

  <main v-else class="trace-page">
    <section class="run-strip"><button class="back-button" @click="back">←</button><div class="run-title"><span>{{ selected.id }}</span><strong>{{ selected.request }}</strong></div><span class="status-chip" :data-status="selected.state"><i />{{ selected.state }}</span><span class="stat">{{ selected.repository_value }}</span></section>
    <nav class="trace-controls">
      <button v-if="selected.state === 'draft'" class="primary" @click="command('start')">Start</button>
      <button v-if="selected.state === 'awaiting_plan_approval'" class="primary" @click="command('approve')">Approve plan</button>
      <button v-if="active" @click="command('pause')">Pause</button><button v-if="selected.state === 'paused' || selected.state === 'blocked'" @click="command('resume')">Resume</button>
      <button v-if="!['completed','aborted'].includes(selected.state)" @click="command('abort')">Abort</button><button v-if="inactive" @click="remove">Delete</button>
    </nav>
    <section class="detail-grid">
      <article class="panel"><p class="kicker">REPOSITORY</p><dl><dt>Workspace</dt><dd>{{ selected.workspace_path || 'created on start' }}</dd><dt>Base SHA</dt><dd>{{ selected.base_sha || '—' }}</dd><dt>Error</dt><dd>{{ selected.error || '—' }}</dd></dl></article>
      <article v-if="plan" class="panel"><p class="kicker">GENERATED PLAN</p><pre>{{ JSON.stringify(JSON.parse(plan.payload), null, 2) }}</pre></article>
      <article class="panel wide"><p class="kicker">PHASES</p><div v-for="phase in phases" :key="phase.id" class="event-row"><span class="campaign-id">{{ phase.sequence }} · {{ phase.owner }}</span><strong>{{ phase.name }}</strong><span class="status-chip">{{ phase.status }}</span><small>{{ phase.error }}</small></div></article>
      <article class="panel wide"><p class="kicker">LIVE TRACE</p><div v-for="event in events" :key="event.id" class="event-row"><span class="campaign-id">{{ event.sequence }} · {{ event.type }}</span><strong>{{ event.name }}</strong><pre>{{ JSON.stringify(event.payload, null, 2) }}</pre></div></article>
      <article class="panel"><p class="kicker">CHECKS</p><div v-for="check in checks" :key="check.id" class="event-row"><strong>{{ check.name }}</strong><span class="status-chip">{{ check.status }}</span><code>{{ check.command }}</code></div></article>
      <article class="panel"><p class="kicker">CHANGED FILES</p><code v-for="file in diff.files" :key="file">{{ file }}</code><pre>{{ diff.patch }}</pre></article>
    </section>
  </main>
</template>
