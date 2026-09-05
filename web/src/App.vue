<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { api } from "./api";
import ArtifactRail from "./components/ArtifactRail.vue";
import ArtifactViewer from "./components/ArtifactViewer.vue";
import ContextComposer from "./components/ContextComposer.vue";
import CreateTaskForm from "./components/CreateTaskForm.vue";
import SessionLog from "./components/SessionLog.vue";
import TaskRail from "./components/TaskRail.vue";
import type { ArtifactView, Branch, Check, CreateSessionInput, CreateTaskInput, Diff, Envelope, Health, Phase, Task, TraceEvent } from "./types";

const eventMemoryLimit = 1_000;
const refreshIntervalMilliseconds = 5_000;
const tasks = ref<Task[]>([]);
const selectedId = ref(routeTaskID());
const creating = ref(location.hash === "#/new" || !selectedId.value);
const selected = computed(() => tasks.value.find((task) => task.id === selectedId.value));
const selectedRoot = computed(() => {
  if (!selected.value?.parent_task_id) return selected.value;
  return tasks.value.find((task) => task.id === selected.value?.parent_task_id);
});
const health = ref<Health>({ status: "loading", errors: [] });
const phases = ref<Phase[]>([]);
const branches = ref<Branch[]>([]);
const events = ref<TraceEvent[]>([]);
const checks = ref<Check[]>([]);
const results = ref<Envelope[]>([]);
const diff = ref<Diff>({ repositories: [] });
const selectedPhase = ref("");
const selectedArtifact = ref<ArtifactView>();
const selectedQuote = ref("");
const composer = ref<{ setText: (text: string) => void }>();
const pendingMessage = ref("");
const creatingSession = ref(false);
const sessionRequest = ref("");

const selectedBranch = computed(() => branches.value.find((branch) => branch.id === selected.value?.selected_branch_id) ?? branches.value[0]);
const expectedHead = computed(() => selectedBranch.value?.head_attempt_id ?? "");
const availableActions = computed<string[] | undefined>(() => {
  if (selectedPhase.value) {
    const match = [...events.value].reverse().find((event) => event.attempt_id === selectedPhase.value || event.phase_id === selectedPhase.value);
    if (match?.available_actions?.length) return match.available_actions;
  }
  const tail = events.value.at(-1);
  return tail?.available_actions?.length ? tail.available_actions : undefined;
});
const targetLabel = computed(() => {
  if (selectedPhase.value) {
    const phase = phases.value.find((item) => item.id === selectedPhase.value);
    return phase ? `Attempt ${phase.name} #${phase.attempt}${phase.superseded ? " · superseded" : ""}` : undefined;
  }
  if (selectedArtifact.value) return `Artifact ${selectedArtifact.value.title}`;
  return undefined;
});
const traceConnected = ref(false);
const busy = ref(false);
const error = ref("");
const eventSequences = new Set<number>();
let timer = 0;
let stream: EventSource | undefined;

const visibleEvents = computed(() => selectedPhase.value ? events.value.filter((event) => event.phase_id === selectedPhase.value) : events.value);

function routeTaskID(): string {
  const sessionMatch = location.hash.match(/^#\/tasks\/[^/]+\/sessions\/([^/]+)/);
  if (sessionMatch) return decodeURIComponent(sessionMatch[1]);
  const match = location.hash.match(/^#\/tasks\/([^/]+)/);
  return match ? decodeURIComponent(match[1]) : "";
}

function openTask(id: string) {
  const task = tasks.value.find((candidate) => candidate.id === id);
  const rootID = task?.parent_task_id || id;
  location.hash = `#/tasks/${encodeURIComponent(rootID)}/sessions/${encodeURIComponent(id)}`;
}
function openCreate() { location.hash = "#/new"; }
function route() {
  selectedId.value = routeTaskID();
  creating.value = location.hash === "#/new" || !selectedId.value;
  creatingSession.value = false;
  sessionRequest.value = "";
}

async function refreshOverview() {
  try {
    const [taskRows, currentHealth] = await Promise.all([api.tasks(), api.health()]);
    tasks.value = taskRows ?? [];
    health.value = { ...currentHealth, errors: currentHealth.errors ?? [] };
  } catch (cause) { error.value = errorMessage(cause); }
}

async function refreshDetail() {
  const id = selectedId.value;
  if (!id) return;
  try {
    const [task, phaseRows, branchRows, checkRows, envelopes, changed] = await Promise.all([api.task(id), api.attempts(id), api.branches(id), api.checks(id), api.results(id), api.diff(id)]);
    if (id !== selectedId.value) return;
    const index = tasks.value.findIndex((item) => item.id === id);
    if (index >= 0) tasks.value[index] = task;
    phases.value = phaseRows ?? [];
    branches.value = branchRows ?? [];
    checks.value = checkRows ?? [];
    results.value = envelopes ?? [];
    diff.value = { repositories: changed.repositories ?? [] };
  } catch (cause) { error.value = errorMessage(cause); }
}

async function loadEventTail(id: string) {
  try {
    const page = await api.events(id, { tail: eventMemoryLimit });
    if (id !== selectedId.value) return;
    events.value = page.events ?? [];
    eventSequences.clear();
    events.value.forEach((event) => eventSequences.add(event.sequence));
  } catch (cause) { error.value = errorMessage(cause); }
}

async function loadSelection(id: string) {
  stream?.close();
  stream = undefined;
  traceConnected.value = false;
  phases.value = [];
  branches.value = [];
  events.value = [];
  checks.value = [];
  results.value = [];
  diff.value = { repositories: [] };
  selectedPhase.value = "";
  selectedArtifact.value = undefined;
  eventSequences.clear();
  if (!id) return;
  await Promise.all([refreshDetail(), loadEventTail(id)]);
  connectStream(id);
}

function connectStream(id: string) {
  const cursor = events.value.at(-1)?.sequence ?? 0;
  stream = new EventSource(`/api/v1/tasks/${encodeURIComponent(id)}/events/stream?after=${cursor}`);
  stream.onopen = () => { if (id === selectedId.value) traceConnected.value = true; };
  stream.addEventListener("event", (message) => {
    if (id !== selectedId.value) return;
    const event = JSON.parse((message as MessageEvent<string>).data) as TraceEvent;
    if (eventSequences.has(event.sequence)) return;
    eventSequences.add(event.sequence);
    events.value.push(event);
    if (events.value.length > eventMemoryLimit) events.value.splice(0, events.value.length - eventMemoryLimit);
  });
  stream.onerror = () => { if (id === selectedId.value) traceConnected.value = false; };
}

async function createTask(value: CreateTaskInput) {
  busy.value = true;
  try { const task = await api.create(value); await refreshOverview(); openTask(task.id); }
  catch (cause) { error.value = errorMessage(cause); }
  finally { busy.value = false; }
}

async function createSession() {
  if (!selectedRoot.value) return;
  const value: CreateSessionInput = { request: sessionRequest.value.trim() };
  if (!value.request) return;
  busy.value = true;
  try {
    const session = await api.createSession(selectedRoot.value.id, value);
    sessionRequest.value = "";
    creatingSession.value = false;
    await refreshOverview();
    openTask(session.id);
  } catch (cause) { error.value = errorMessage(cause); }
  finally { busy.value = false; }
}

async function command(name: string) {
  if (!selected.value) return;
  busy.value = true;
  try { await api.command(selected.value.id, name); await Promise.all([refreshOverview(), refreshDetail()]); }
  catch (cause) { error.value = errorMessage(cause); }
  finally { busy.value = false; }
}

async function composerSend(value: { message: string; action: string }) {
  if (!selected.value) return;
  if (value.action === "feedback") {
    busy.value = true;
    try { await api.feedback(selected.value.id, value.message, selected.value.plan_digest); await refreshDetail(); }
    catch (cause) { error.value = errorMessage(cause); }
    finally { busy.value = false; }
    return;
  }
  if (["start", "approve", "resume"].includes(value.action)) { await command(value.action); return; }
  busy.value = true;
  pendingMessage.value = value.message;
  try {
    const target: { event_id?: string; artifact_id?: string; attempt_id?: string } = {};
    if (selectedPhase.value) target.attempt_id = selectedPhase.value;
    const result = await api.intervene(selected.value.id, { target, intent: value.action, message: value.message, expected_branch_head: expectedHead.value || undefined, idempotency_key: crypto.randomUUID() });
    pendingMessage.value = "";
    selectedQuote.value = "";
    await Promise.all([refreshOverview(), refreshDetail(), loadEventTail(selected.value.id)]);
    if (result.branch_id) {
      const branch = branches.value.find((item) => item.id === result.branch_id);
      if (branch?.head_attempt_id) selectedPhase.value = branch.head_attempt_id;
      else if (result.attempt_id) selectedPhase.value = result.attempt_id;
    }
  } catch (cause) {
    error.value = errorMessage(cause);
    composer.value?.setText(pendingMessage.value);
    await Promise.all([refreshOverview(), refreshDetail(), loadEventTail(selected.value.id)]);
  }
  finally { busy.value = false; }
}

async function removeTask() {
  if (!selected.value) return;
  const rootID = selected.value.parent_task_id;
  busy.value = true;
  try {
    await api.remove(selected.value.id);
    await refreshOverview();
    if (rootID) openTask(rootID);
    else openCreate();
  }
  catch (cause) { error.value = errorMessage(cause); }
  finally { busy.value = false; }
}

function errorMessage(cause: unknown): string { return cause instanceof Error ? cause.message : String(cause); }

watch(selectedId, (id) => void loadSelection(id));
onMounted(async () => {
  window.addEventListener("hashchange", route);
  await api.initialize();
  await refreshOverview();
  await loadSelection(selectedId.value);
  timer = window.setInterval(() => void Promise.all([refreshOverview(), refreshDetail()]), refreshIntervalMilliseconds);
});
onUnmounted(() => { window.removeEventListener("hashchange", route); clearInterval(timer); stream?.close(); });
</script>

<template>
  <div class="task-shell" :data-context-rail="selected ? 'open' : 'closed'">
    <TaskRail :tasks="tasks" :selected-id="selectedId" @select="openTask" @create="openCreate" />
    <CreateTaskForm v-if="creating || !selected" :disabled="busy || health.status !== 'ok'" @submit="createTask" />
    <main v-else class="task-workspace">
      <header class="workspace-breadcrumb"><button type="button" @click="openCreate">Tasks</button><b>›</b><strong>{{ selectedRoot?.request }}</strong><b>›</b><strong>{{ selected.request }}</strong><span class="health-state" :data-state="health.status">● {{ health.status }}</span></header>
      <form v-if="creatingSession" class="session-creator" @submit.prevent="createSession">
        <label for="session-request">New session</label>
        <textarea id="session-request" v-model="sessionRequest" autofocus required placeholder="What should this session do?" />
        <div><button class="text-button" type="button" @click="creatingSession = false; sessionRequest = ''">Cancel</button><button class="button button--accent" type="submit" :disabled="busy || !sessionRequest.trim()">Create session</button></div>
      </form>
      <section class="task-header">
        <div class="task-header__title"><p>Session <span>{{ selected.id }}</span></p><h1>{{ selected.request }}</h1></div>
        <div class="task-header__actions">
          <span class="state-badge" :data-state="selected.state">{{ selected.state.replaceAll('_', ' ') }}</span>
          <button class="button button--accent" type="button" :disabled="busy || health.status !== 'ok'" @click="creatingSession = !creatingSession">New session</button>
          <button v-if="['preparing','planning','building','checking','reviewing'].includes(selected.state)" class="button" type="button" @click="command('pause')">Pause</button>
          <button v-if="!['completed','aborted'].includes(selected.state)" class="button" type="button" @click="command('abort')">Abort</button>
          <button v-else class="button" type="button" @click="removeTask">Delete</button>
        </div>
        <dl class="task-facts">
          <div><dt>Workspace</dt><dd>{{ selected.workspace_path }}</dd></div>
          <div><dt>Repositories</dt><dd>{{ selected.repositories.length }}</dd></div>
          <div><dt>Branch</dt><dd>{{ selectedBranch?.id?.slice(0, 8) ?? '—' }} · head {{ (selectedBranch?.head_attempt_id ?? '').slice(0, 8) || '—' }}</dd></div>
          <div><dt>Current attempt</dt><dd>{{ phases.at(-1)?.name || 'not started' }}</dd></div>
          <div><dt>Checks</dt><dd>{{ checks.filter((check) => check.status === 'passed').length }}/{{ checks.length }}</dd></div>
        </dl>
        <div class="repository-chips"><span v-for="repository in selected.repositories" :key="repository.id" :data-primary="repository.primary"><b>{{ repository.primary ? '◆' : '◇' }}</b>{{ repository.name }}<small>{{ repository.source_type }}</small></span></div>
      </section>
      <ArtifactViewer v-if="selectedArtifact" :artifact="selectedArtifact" @close="selectedArtifact = undefined" @comment="selectedQuote = $event" />
      <template v-else>
        <section class="attempt-table">
          <header><span>Status</span><span>Attempt</span><span>Owner</span><span>Evidence</span></header>
          <button v-for="phase in phases" :key="phase.id" :class="{ selected: selectedPhase === phase.id }" type="button" @click="selectedPhase = selectedPhase === phase.id ? '' : phase.id">
            <span class="attempt-status" :data-state="phase.status">● {{ phase.status }}</span><strong>{{ phase.name }}</strong><span>{{ phase.owner }}</span><small>{{ phase.error || phase.description }}</small>
          </button>
          <div v-if="phases.length === 0" class="workspace-empty"><strong>Task workspace ready</strong><span>Start the Task to create its first repository materialization and attempt.</span></div>
        </section>
        <SessionLog v-if="events.length" :events="visibleEvents" :live="traceConnected" />
      </template>
      <ContextComposer ref="composer" :state="selected.state" :quote="selectedQuote" :busy="busy" :available-actions="availableActions" :target-label="targetLabel" :expected-head="expectedHead" @send="composerSend" />
    </main>
    <ArtifactRail v-if="selected" :phases="phases" :results="results" :checks="checks" :diff="diff" :selected-phase="selectedPhase" @select-phase="selectedPhase = $event" @select-artifact="selectedArtifact = $event" />
    <div v-if="error || health.errors.length" class="toast" role="alert"><button type="button" @click="error = ''">×</button>{{ error || health.errors.join(' · ') }}</div>
  </div>
</template>
