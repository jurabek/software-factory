<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { ArrowLeft, CheckCircle2, Clock3, GitBranch, TriangleAlert, Wrench } from "@lucide/vue";
import { api } from "../api";
import type { AgentRun, Campaign, CheckRow, ControlState, FindingRow, LiveRun, LiveSnapshot, Phase, TraceEvent } from "../types";
import { formatDuration, isRunning, traceTypeGroups } from "../types";
import SessionLog from "./SessionLog.vue";
import TraceControls from "./TraceControls.vue";

const props = defineProps<{ campaign: Campaign; control: ControlState }>();
defineEmits<{ back: [] }>();

const campaign = ref(props.campaign);
const phases = ref<Phase[]>([]);
const agents = ref<AgentRun[]>([]);
const checks = ref<CheckRow[]>([]);
const findings = ref<FindingRow[]>([]);
const live = ref<LiveSnapshot>({ generatedAt: "", staleAfterMs: 15_000, runs: [] });
const events = ref<TraceEvent[]>([]);
const selectedGroups = ref(Object.keys(traceTypeGroups));
const role = ref("");
const runId = ref("");
const paused = ref(false);
const showPayloads = ref(false);
const error = ref("");
const approvalMessage = ref("");
const approving = ref(false);
const connected = ref(false);
const now = ref(Date.now());
let cursor = 0;
let timer: number | undefined;
let inflight = false;

const roles = computed(() => [...new Set(agents.value.map((agent) => agent.role))]);
const runs = computed(() => agents.value.map((agent) => ({
  id: agent.id,
  label: `${agent.role}${agent.work_item_id ? ` / ${agent.work_item_id}` : ""} · ${agent.id}`,
})));
const selectedTypes = computed(() =>
  selectedGroups.value.flatMap((group) => traceTypeGroups[group as keyof typeof traceTypeGroups] ?? []),
);
const filteredEvents = computed(() => {
  const allowed = new Set<string>(selectedTypes.value);
  return events.value.filter((event) =>
    allowed.has(event.type) &&
    (!role.value || event.payload.role === role.value) &&
    (!runId.value || event.payload.runId === runId.value),
  );
});

const timeline = computed(() => {
  const started = phases.value.map((phase) => Date.parse(phase.started_at ?? "")).filter(Number.isFinite);
  const ended = phases.value.map((phase) =>
    phase.completed_at ? Date.parse(phase.completed_at) : now.value,
  ).filter(Number.isFinite);
  const start = Math.min(...started, now.value);
  const end = Math.max(...ended, start + 1_000);
  return { start, end, span: Math.max(end - start, 1_000) };
});

const lanes = computed(() => {
  const output = new Map<string, Phase[]>();
  for (const phase of phases.value) {
    const label = `${phase.role ?? phase.kind}${phase.work_item_id ? ` / ${phase.work_item_id}` : ""}`;
    output.set(label, [...(output.get(label) ?? []), phase]);
  }
  return [...output.entries()];
});

const duration = computed(() => {
  const start = Date.parse(campaign.value.createdAt);
  const end = isRunning(campaign.value.state) ? now.value : Date.parse(campaign.value.updatedAt);
  return formatDuration(end - start);
});

function phaseStyle(phase: Phase): Record<string, string> {
  const start = Date.parse(phase.started_at ?? "");
  if (!Number.isFinite(start)) return { left: "90%", width: "9%" };
  const end = phase.completed_at ? Date.parse(phase.completed_at) : now.value;
  const rawLeft = ((start - timeline.value.start) / timeline.value.span) * 100;
  const width = Math.min(Math.max(((end - start) / timeline.value.span) * 100, 8), 99);
  const left = Math.min(Math.max(rawLeft, 0), 99 - width);
  return {
    left: `${left}%`,
    width: `${width}%`,
  };
}

function phaseDuration(phase: Phase): string {
  const start = Date.parse(phase.started_at ?? "");
  const end = phase.completed_at ? Date.parse(phase.completed_at) : now.value;
  return formatDuration(end - start);
}

function progressValue(run: LiveRun, key: "thinkingChars" | "textChars"): number {
  const value = run.modelRequest?.progress?.[key];
  return typeof value === "number" ? value : 0;
}

function activeDetail(run: LiveRun): string {
  if (run.stage === "reasoning") return `${formatDuration(run.modelRequest?.elapsedMs ?? 0)} in model`;
  if (run.stage === "tool") {
    const name = run.activeTool?.toolName;
    return typeof name === "string" ? name : "tool running";
  }
  return "between operations";
}

const awaitingPlanApproval = computed(() => campaign.value.state === "awaiting_plan_approval");
const approvalCommand = computed(() => `npm run dev -- approve ${campaign.value.id} plan`);

async function approvePlan() {
  if (!props.control.enabled || !props.control.token || approving.value) return;
  if (!window.confirm(`Approve the plan for ${campaign.value.id}? This starts the Builder stage.`)) return;
  approving.value = true;
  approvalMessage.value = "";
  try {
    const result = await api.approvePlan(campaign.value.id, props.control.token);
    campaign.value = result.campaign;
    approvalMessage.value = "Plan approved. Builders can now run.";
    await tick();
  } catch (cause) {
    approvalMessage.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    approving.value = false;
  }
}

async function copyApprovalCommand() {
  try {
    await navigator.clipboard.writeText(approvalCommand.value);
    approvalMessage.value = "Approval command copied.";
  } catch {
    approvalMessage.value = approvalCommand.value;
  }
}

function toolTicks(phase: Phase): number[] {
  const start = Date.parse(phase.started_at ?? "");
  const end = phase.completed_at ? Date.parse(phase.completed_at) : now.value;
  const span = Math.max(end - start, 1);
  return events.value
    .filter((event) => event.type === "tool_start" && event.payload.runId === phase.id)
    .map((event) => Math.min(Math.max(((Date.parse(event.created_at) - start) / span) * 100, 2), 98));
}

async function tick() {
  if (inflight || paused.value) return;
  inflight = true;
  try {
    const id = campaign.value.id;
    const [detail, phaseRows, agentRows, checkRows, findingRows, liveSnapshot] = await Promise.all([
      api.campaign(id),
      api.phases(id),
      api.agents(id),
      api.checks(id),
      api.findings(id),
      api.live(id),
    ]);
    campaign.value = detail.campaign;
    phases.value = phaseRows;
    agents.value = agentRows;
    checks.value = checkRows;
    findings.value = findingRows;
    live.value = liveSnapshot;
    let page;
    do {
      page = await api.events(id, { after: cursor, limit: 500 });
      cursor = Math.max(cursor, page.cursor);
      if (page.events.length) events.value = [...events.value, ...page.events];
    } while (page.hasMore);
    connected.value = true;
    error.value = "";
    now.value = Date.now();
  } catch (cause) {
    connected.value = false;
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    inflight = false;
  }
}

onMounted(() => {
  void tick();
  timer = window.setInterval(() => void tick(), 500);
});
onUnmounted(() => window.clearInterval(timer));
</script>

<template>
  <section class="trace-page">
    <div v-if="error" class="error-bar">API unreachable — retrying: {{ error }}</div>
    <div class="run-strip">
      <button class="back-button" title="Back to campaigns" @click="$emit('back')"><ArrowLeft :size="20" /></button>
      <div class="run-title">
        <span><GitBranch :size="15" />{{ campaign.id }}</span>
        <strong>{{ campaign.title }}</strong>
      </div>
      <span class="status-chip" :data-status="campaign.state"><i />{{ campaign.state.replaceAll("_", " ") }}</span>
      <span class="stat"><Clock3 :size="16" />{{ duration }}</span>
      <span class="stat"><Wrench :size="16" />{{ campaign.repairCycles }} repairs</span>
      <span class="stat"><CheckCircle2 :size="16" />{{ checks.filter((item) => item.status === "passed").length }}/{{ checks.length }} checks</span>
      <span class="stat"><TriangleAlert :size="16" />{{ findings.filter((item) => !item.resolved).length }} findings</span>
    </div>

    <section v-if="awaitingPlanApproval" class="approval-panel">
      <div>
        <p class="kicker">HUMAN CHECKPOINT</p>
        <h2>Plan approval is ready</h2>
        <p>The Planner has completed. Review the request and approve this campaign without retyping its ID.</p>
      </div>
      <div class="approval-actions">
        <button v-if="control.enabled" class="approve-button" :disabled="approving" @click="approvePlan">
          {{ approving ? "Approving…" : "Approve plan" }}
        </button>
        <button v-else class="copy-button" @click="copyApprovalCommand">Copy approval command</button>
        <small v-if="!control.enabled">For one-click approval, restart with <code>swf-ui --control</code>.</small>
        <small v-if="approvalMessage" class="approval-message">{{ approvalMessage }}</small>
      </div>
    </section>

    <section v-if="live.runs.length" class="live-runs-panel">
      <div class="live-runs-head">
        <div>
          <p class="kicker">RUNTIME LIVENESS</p>
          <h2>Active agent processing</h2>
        </div>
        <small>stale after {{ formatDuration(live.staleAfterMs) }} without a signal</small>
      </div>
      <div class="live-runs-grid">
        <article v-for="active in live.runs" :key="active.id" class="live-run-card" :class="{ stale: active.stale }">
          <div class="live-run-title">
            <strong>{{ active.role }}<span v-if="active.workItemId"> / {{ active.workItemId }}</span></strong>
            <span class="runtime-stage" :data-stage="active.stale ? 'stale' : active.stage">
              {{ active.stale ? "stale" : active.stage }}
            </span>
          </div>
          <p>{{ activeDetail(active) }}</p>
          <dl>
            <div><dt>run</dt><dd>{{ formatDuration(active.elapsedMs) }}</dd></div>
            <div><dt>last signal</dt><dd>{{ formatDuration(active.lastActivityMs) }} ago</dd></div>
            <div><dt>reasoning stream</dt><dd>{{ progressValue(active, "thinkingChars").toLocaleString() }} chars</dd></div>
            <div><dt>answer stream</dt><dd>{{ progressValue(active, "textChars").toLocaleString() }} chars</dd></div>
          </dl>
          <div class="process-list">
            <span v-for="process in active.processes" :key="process.id" :class="{ ended: !process.running }">
              PID {{ process.pid }} · {{ process.kind }} · {{ process.running ? "running" : "ended" }}
            </span>
            <span v-if="!active.processes.length">no process record</span>
          </div>
        </article>
      </div>
    </section>

    <TraceControls
      v-model:selectedGroups="selectedGroups"
      v-model:role="role"
      v-model:runId="runId"
      v-model:paused="paused"
      v-model:showPayloads="showPayloads"
      :roles="roles"
      :runs="runs"
    />

    <section class="waterfall">
      <div class="axis-row">
        <span>agent sessions</span>
        <div>
          <i v-for="tick in 4" :key="tick" :style="{ left: `${tick * 20}%` }">{{ formatDuration(timeline.span * tick / 5) }}</i>
        </div>
      </div>
      <div v-for="[lane, lanePhases] in lanes" :key="lane" class="lane-row">
        <label>{{ lane }}</label>
        <div class="lane-track">
          <span v-for="tick in 4" :key="tick" class="gridline" :style="{ left: `${tick * 20}%` }" />
          <button
            v-for="phase in lanePhases"
            :key="phase.id"
            class="phase-block"
            :class="[phase.status, { selected: runId === phase.id }]"
            :style="phaseStyle(phase)"
            @click="runId = runId === phase.id ? '' : phase.id"
          >
            <span><b>{{ phase.status === "running" ? "●" : phase.status === "passed" ? "✓" : "✗" }}</b>{{ phase.kind }}</span>
            <small>{{ phaseDuration(phase) }}</small>
            <i v-for="position in toolTicks(phase)" :key="position" class="tool-tick" :style="{ left: `${position}%` }" />
          </button>
        </div>
      </div>
      <div v-if="lanes.length === 0" class="empty-state">waiting for agent phases…</div>
    </section>

    <SessionLog :events="filteredEvents" :show-payloads="showPayloads" :live="connected && !paused" />
  </section>
</template>
