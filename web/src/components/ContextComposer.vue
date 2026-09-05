<script setup lang="ts">
import { computed, ref, watch } from "vue";

const props = defineProps<{ state: string; quote?: string; busy: boolean; availableActions?: string[]; targetLabel?: string; expectedHead?: string }>();
const emit = defineEmits<{ send: [value: { message: string; action: string }] }>();
const message = ref("");
const action = ref("comment");

const actionLabels: Record<string, string> = {
  comment: "Comment",
  steer: "Steer running agent",
  follow_up: "Follow up after settle",
  retry: "Retry exact",
  revise: "Revise and retry",
  repair: "Continue repair",
  feedback: "Revise plan",
  approve: "Approve plan",
  start: "Start",
  resume: "Resume",
};

const actions = computed(() => {
  if (props.state === "awaiting_plan_approval") return [{ value: "feedback", label: actionLabels.feedback }, { value: "approve", label: actionLabels.approve }];
  if (props.state === "draft") return [{ value: "start", label: actionLabels.start }, { value: "comment", label: actionLabels.comment }];
  if (["blocked", "paused"].includes(props.state)) {
    const values = [{ value: "resume", label: actionLabels.resume }, { value: "comment", label: actionLabels.comment }];
    for (const item of props.availableActions ?? []) {
      if (["retry", "revise", "repair"].includes(item) && !values.some((entry) => entry.value === item)) {
        values.push({ value: item, label: actionLabels[item] ?? item });
      }
    }
    return values;
  }
  const server = props.availableActions ?? [];
  if (server.length > 0) return server.map((item) => ({ value: item, label: actionLabels[item] ?? item }));
  return [{ value: "comment", label: actionLabels.comment }];
});

const consequence = computed(() => {
  switch (action.value) {
    case "retry": return "Creates a child branch from the input snapshot; history stays immutable.";
    case "revise": return "Creates a new definition revision and attempt on a child branch.";
    case "repair": return "Starts a Builder repair from the output snapshot, then reruns checks.";
    case "steer": return "Delivers after the current tool turn; Intervention stays anchored.";
    case "follow_up": return "Delivers after the running agent settles.";
    case "comment": return "Stores the anchored message without scheduling work.";
    default: return "Message applies to the selected Task history.";
  }
});

watch(actions, (values) => { if (!values.some((item) => item.value === action.value)) action.value = values[0]?.value ?? "comment"; }, { immediate: true });
watch(() => props.quote, (quote) => { if (quote) message.value = `Regarding “${quote}”\n`; });

function submit() {
  if (!message.value.trim() && !["start", "approve", "resume", "retry"].includes(action.value)) return;
  emit("send", { message: message.value.trim(), action: action.value });
  if (action.value !== "comment") return;
  message.value = "";
}

defineExpose({ keepText: () => undefined, setText: (text: string) => { message.value = text; } });
</script>

<template>
  <form class="context-composer" :data-intent="action" @submit.prevent="submit">
    <header><span :data-state="state">● {{ state.replaceAll('_', ' ') }}</span><select v-model="action"><option v-for="item in actions" :key="item.value" :value="item.value">{{ item.label }}</option></select></header>
    <p v-if="targetLabel" class="composer-target">{{ targetLabel }}<span v-if="expectedHead"> · head {{ expectedHead.slice(0, 8) }}</span></p>
    <textarea v-model="message" :placeholder="action === 'feedback' ? 'Answer the planner questions…' : 'Message this task…'" @keydown.ctrl.enter.prevent="submit" />
    <footer><span>{{ consequence }}</span><button class="button button--send" :disabled="busy" type="submit">Send <kbd>CTRL+ENTER</kbd></button></footer>
  </form>
</template>
