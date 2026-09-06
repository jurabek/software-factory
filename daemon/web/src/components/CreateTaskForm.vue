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
const recentDirectories = ref<string[]>([]);
const repositoryControl = ref<HTMLDetailsElement>();
const agentControl = ref<HTMLDetailsElement>();

const recentDirectoriesStorageKey = "software-factory.recent-directories";

const providers = computed(() => {
  const names = new Set(models.value.map((item) => item.provider));
  return [...names].sort();
});

const primaryRepository = computed(() => repositories.value.find((repository) => repository.primary) ?? repositories.value[0]);
const repositoryLabel = computed(() => {
  const repository = primaryRepository.value;
  if (repository.type === "github") return repository.repo || "Choose repository";
  return repository.path || "Choose directory";
});
const agentLabel = computed(() => {
  const selectedModel = model.value.split("/").at(-1) || "model";
  return `${codingAgent.value} / ${selectedModel} / ${thinking.value}`.toUpperCase();
});
const repositoriesValid = computed(() => repositories.value.every((repository) =>
  repository.type === "local" ? Boolean(repository.path?.trim()) : Boolean(repository.repo?.trim()),
));

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

function selectRecentDirectory(path: string) {
  primaryRepository.value.type = "local";
  primaryRepository.value.path = path;
}

function rememberDirectories() {
  const selectedDirectories = repositories.value
    .filter((repository) => repository.type === "local" && repository.path?.trim())
    .map((repository) => repository.path!.trim());
  recentDirectories.value = [...new Set([...selectedDirectories, ...recentDirectories.value])].slice(0, 6);
  localStorage.setItem(recentDirectoriesStorageKey, JSON.stringify(recentDirectories.value));
}

function closeOtherControl(control: "repository" | "agent", event: ToggleEvent) {
  if (!(event.currentTarget as HTMLDetailsElement).open) return;
  const otherControl = control === "repository" ? agentControl.value : repositoryControl.value;
  if (otherControl) otherControl.open = false;
}

function submit() {
  if (!repositoriesValid.value) return;
  rememberDirectories();
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
    const storedDirectories = JSON.parse(localStorage.getItem(recentDirectoriesStorageKey) ?? "[]");
    if (Array.isArray(storedDirectories)) recentDirectories.value = storedDirectories.filter((path): path is string => typeof path === "string");
  } catch {
    localStorage.removeItem(recentDirectoriesStorageKey);
  }
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
      <footer class="composer-footer">
        <details ref="repositoryControl" class="compact-control repository-control" @toggle="closeOtherControl('repository', $event)">
          <summary><span aria-hidden="true">▱</span><strong>{{ repositoryLabel }}</strong><span aria-hidden="true">⌄</span></summary>
          <section class="compact-popover directory-popover" aria-label="Task repositories">
            <header>Repository</header>
            <div v-for="(repository, index) in repositories" :key="index" class="repository-input">
              <button class="primary-selector" :aria-pressed="repository.primary" type="button" title="Set primary repository" @click="selectPrimary(index)">{{ repository.primary ? '◆' : '◇' }}</button>
              <input v-model="repository.name" aria-label="Repository name" placeholder="name (optional)" />
              <select v-model="repository.type" aria-label="Repository source">
                <option value="local">Local</option>
                <option value="github">GitHub</option>
              </select>
              <input v-if="repository.type === 'local'" v-model="repository.path" aria-label="Local repository path" placeholder="Type a directory path…" />
              <input v-else v-model="repository.repo" aria-label="GitHub repository" placeholder="owner/repository" />
              <button class="icon-button" :disabled="repositories.length === 1" type="button" aria-label="Remove repository" @click="removeRepository(index)">×</button>
            </div>
            <template v-if="recentDirectories.length">
              <h2>Recent</h2>
              <button v-for="path in recentDirectories" :key="path" class="recent-directory" type="button" @click="selectRecentDirectory(path)"><span aria-hidden="true">◷</span>{{ path }}</button>
            </template>
            <button class="text-button" type="button" @click="addRepository">＋ Add repository</button>
          </section>
        </details>
        <span class="repository-count">{{ repositories.length }} {{ repositories.length === 1 ? 'repository' : 'repositories' }}</span>
        <details ref="agentControl" class="compact-control agent-control" @toggle="closeOtherControl('agent', $event)">
          <summary><strong>{{ agentLabel }}</strong><span aria-hidden="true">⌄</span></summary>
          <section class="compact-popover agent-popover" aria-label="Agent configuration">
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
                  <option v-for="item in modelsForProvider(provider)" :key="`${item.provider}/${item.id}`" :value="modelValue(item)">{{ item.id }}</option>
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
        </details>
        <button class="button button--send" :disabled="disabled || !request.trim() || !repositoriesValid" type="submit">Create task <kbd>CTRL+ENTER</kbd></button>
      </footer>
    </form>
  </main>
</template>
