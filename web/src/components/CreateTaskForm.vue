<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { api } from "../api";
import type { CreateTaskInput, ModelInfo, RepositoryInput } from "../types";
import { thinkingLevels } from "../types";

defineProps<{ disabled: boolean }>();
const emit = defineEmits<{ submit: [value: CreateTaskInput] }>();

const request = ref("");
const repositories = ref<RepositoryInput[]>([{ type: "local", path: "", primary: true }]);

const harnesses = ref<string[]>(["pi"]);
const codingAgent = ref("pi");
const models = ref<ModelInfo[]>([]);
const model = ref("");
const thinking = ref("medium");
const modelsError = ref("");

const providers = computed(() => {
  const names = new Set(models.value.map((item) => item.provider));
  return [...names].sort();
});

function modelsForProvider(provider: string): ModelInfo[] {
  return models.value.filter((item) => item.provider === provider);
}

function modelValue(item: ModelInfo): string {
  return `${item.provider}/${item.id}`;
}

async function loadModels(harness: string, preferred?: string) {
  models.value = [];
  modelsError.value = "";
  try {
    const response = await api.models(harness);
    models.value = response.models ?? [];
  } catch (cause) {
    modelsError.value = cause instanceof Error ? cause.message : String(cause);
    return;
  }
  if (preferred && models.value.some((item) => modelValue(item) === preferred || item.id === preferred)) {
    model.value = preferred;
    return;
  }
  if (preferred && !models.value.length) {
    model.value = preferred;
    return;
  }
  if (!model.value || !models.value.some((item) => modelValue(item) === model.value || item.id === model.value)) {
    model.value = models.value.length ? modelValue(models.value[0]) : (preferred ?? "");
  }
}

function addRepository() {
  repositories.value.push({ type: "local", path: "" });
}

function removeRepository(index: number) {
  if (repositories.value.length === 1) return;
  const removedPrimary = repositories.value[index].primary;
  repositories.value.splice(index, 1);
  if (removedPrimary) repositories.value[0].primary = true;
}

function selectPrimary(index: number) {
  repositories.value.forEach((repository, repositoryIndex) => { repository.primary = repositoryIndex === index; });
}

function submit() {
  const value: CreateTaskInput = {
    request: request.value,
    repositories: repositories.value.map((repository) => ({ ...repository })),
    coding_agent: codingAgent.value || undefined,
    model: model.value.trim() || undefined,
    thinking: thinking.value || undefined,
  };
  emit("submit", value);
}

watch(codingAgent, (harness) => void loadModels(harness));

onMounted(async () => {
  try {
    const [harnessResponse, configResponse] = await Promise.all([
      api.harnesses().catch(() => ({ harnesses: ["pi"] as string[] })),
      api.config().catch(() => null),
    ]);
    if (harnessResponse.harnesses?.length) harnesses.value = harnessResponse.harnesses;
    const defaults = configResponse?.config.defaults;
    if (defaults?.coding_agent && harnesses.value.includes(defaults.coding_agent)) codingAgent.value = defaults.coding_agent;
    if (defaults?.thinking) thinking.value = defaults.thinking;
    await loadModels(codingAgent.value, defaults?.model);
  } catch {
    await loadModels(codingAgent.value);
  }
});
</script>

<template>
  <main class="create-workspace">
    <header class="workspace-breadcrumb"><span>Tasks</span><b>›</b><strong>New</strong></header>
    <form class="task-composer task-composer--create" @submit.prevent="submit">
      <p class="composer-eyebrow">New task</p>
      <h1>What should we build today?</h1>
      <label class="composer-prompt">
        <span class="sr-only">Task description</span>
        <textarea v-model="request" required autofocus placeholder="Describe a task…" />
      </label>
      <section class="agent-inputs" aria-label="Agent configuration">
        <label>
          <span>Agent</span>
          <select v-model="codingAgent" aria-label="Coding agent">
            <option v-for="harness in harnesses" :key="harness" :value="harness">{{ harness.toUpperCase() }}</option>
          </select>
        </label>
        <label>
          <span>Model</span>
          <select v-if="models.length" v-model="model" aria-label="Model">
            <optgroup v-for="provider in providers" :key="provider" :label="provider">
              <option v-for="item in modelsForProvider(provider)" :key="`${item.provider}/${item.id}`" :value="modelValue(item)">
                {{ item.id }}
              </option>
            </optgroup>
          </select>
          <input v-else v-model="model" aria-label="Model" placeholder="provider/model" />
        </label>
        <label>
          <span>Thinking</span>
          <select v-model="thinking" aria-label="Thinking level">
            <option v-for="level in thinkingLevels" :key="level" :value="level">{{ level.toUpperCase() }}</option>
          </select>
        </label>
        <small v-if="modelsError" class="agent-inputs__error" role="status">{{ modelsError }}</small>
      </section>
      <section class="repository-inputs" aria-label="Task repositories">
        <div v-for="(repository, index) in repositories" :key="index" class="repository-input">
          <button class="primary-selector" :aria-pressed="repository.primary" type="button" title="Set primary repository" @click="selectPrimary(index)">{{ repository.primary ? '◆' : '◇' }}</button>
          <input v-model="repository.name" aria-label="Repository name" placeholder="name (optional)" />
          <select v-model="repository.type" aria-label="Repository source">
            <option value="local">Local</option>
            <option value="github">GitHub</option>
          </select>
          <input v-if="repository.type === 'local'" v-model="repository.path" required aria-label="Local repository path" placeholder="/absolute/repository/path" />
          <input v-else v-model="repository.repo" required aria-label="GitHub repository" placeholder="owner/repository" />
          <button class="icon-button" :disabled="repositories.length === 1" type="button" aria-label="Remove repository" @click="removeRepository(index)">×</button>
        </div>
        <button class="text-button" type="button" @click="addRepository">＋ Add repository</button>
      </section>
      <footer class="composer-footer">
        <span>◆ primary repository</span>
        <span>{{ codingAgent.toUpperCase() }} / {{ model || '—' }} / {{ thinking.toUpperCase() }}</span>
        <span>{{ repositories.length }} {{ repositories.length === 1 ? 'repository' : 'repositories' }}</span>
        <button class="button button--send" :disabled="disabled || !request.trim()" type="submit">Create task <kbd>CTRL+ENTER</kbd></button>
      </footer>
    </form>
  </main>
</template>
