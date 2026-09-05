<script setup lang="ts">
import type { Task } from "../types";

defineProps<{ tasks: Task[]; selectedId: string }>();
defineEmits<{ select: [id: string]; create: [] }>();

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
    <p class="rail-label">Tasks <span>{{ tasks.length }}</span></p>
    <nav class="task-list">
      <button
        v-for="task in tasks"
        :key="task.id"
        class="task-row"
        :aria-current="selectedId === task.id ? 'page' : undefined"
        :data-state="task.state"
        type="button"
        @click="$emit('select', task.id)"
      >
        <span class="task-row__state" />
        <span class="task-row__copy"><strong>{{ task.request }}</strong><small>{{ shortSource(task) }} · {{ task.state.replaceAll('_', ' ') }}</small></span>
        <time :datetime="task.created_at">{{ task.state === 'draft' ? 'draft' : 'now' }}</time>
      </button>
      <div v-if="tasks.length === 0" class="rail-empty">No tasks yet.<br />Create one to populate this workspace.</div>
    </nav>
    <footer class="task-rail__footer"><span>▣</span> Local factory <strong>PROD</strong></footer>
  </aside>
</template>
