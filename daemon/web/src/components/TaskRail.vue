<script setup lang="ts">
import { computed, ref } from "vue";
import type { Task } from "../types";

const props = defineProps<{ tasks: Task[]; selectedId: string }>();
defineEmits<{ select: [id: string]; create: [] }>();

const rootTasks = computed(() => props.tasks.filter((task) => !task.parent_task_id));
const collapsedTaskIDs = ref(new Set<string>());

function sessionsFor(task: Task): Task[] {
  return props.tasks
    .filter((candidate) => candidate.id === task.id || candidate.parent_task_id === task.id)
    .sort((left, right) => left.created_at.localeCompare(right.created_at));
}

function containsSelection(task: Task): boolean {
  return sessionsFor(task).some((session) => session.id === props.selectedId);
}

function toggleTask(taskID: string): void {
  const next = new Set(collapsedTaskIDs.value);
  if (next.has(taskID)) next.delete(taskID);
  else next.add(taskID);
  collapsedTaskIDs.value = next;
}

function shortSource(task: Task): string {
  if (task.repositories.length === 0) return "workspace only";
  const primary = task.repositories.find((repository) => repository.primary) ?? task.repositories[0];
  const extra = task.repositories.length > 1 ? ` +${task.repositories.length - 1}` : "";
  return `${primary.name}${extra}`;
}
</script>

<template>
  <aside class="task-rail" aria-label="Tasks">
    <header class="task-rail__header">
      <span class="factory-mark" aria-label="Software Factory">SF</span>
      <button class="rail-search" type="button" aria-label="Search tasks">⌕ <kbd>⌘K</kbd></button>
      <button class="button button--accent" type="button" @click="$emit('create')">Create task <kbd>T</kbd></button>
    </header>
    <p class="rail-label">Tasks <span>{{ rootTasks.length }}</span></p>
    <nav class="task-list">
      <section v-for="task in rootTasks" :key="task.id" class="task-group" :data-selected="containsSelection(task)">
        <button class="task-group__row" type="button" :aria-expanded="!collapsedTaskIDs.has(task.id)" @click="toggleTask(task.id)">
          <span class="task-group__chevron">{{ collapsedTaskIDs.has(task.id) ? '›' : '⌄' }}</span>
          <span class="task-row__copy"><strong>{{ task.request }}</strong><small>{{ shortSource(task) }}</small></span>
          <span class="task-group__count">{{ sessionsFor(task).length }}</span>
        </button>
        <div v-if="!collapsedTaskIDs.has(task.id)" class="session-tree" role="group" :aria-label="`${task.request} sessions`">
          <button
            v-for="session in sessionsFor(task)"
            :key="session.id"
            class="task-row task-row--session"
            :aria-current="selectedId === session.id ? 'page' : undefined"
            :data-state="session.state"
            type="button"
            @click="$emit('select', session.id)"
          >
            <span class="task-row__state" />
            <span class="task-row__copy"><strong>{{ session.request }}</strong><small>{{ session.state.replaceAll('_', ' ') }}</small></span>
            <time :datetime="session.created_at">{{ session.state === 'draft' ? 'draft' : 'now' }}</time>
          </button>
        </div>
      </section>
      <div v-if="rootTasks.length === 0" class="rail-empty">No tasks yet.<br />Create one to populate this workspace.</div>
    </nav>
    <footer class="task-rail__footer"><span>▣</span> Local factory <strong>PROD</strong></footer>
  </aside>
</template>
