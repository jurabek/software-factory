<script setup lang="ts">
import { Activity, GitBranch, Wrench } from "@lucide/vue";
import type { Campaign } from "../types";
import { isRunning } from "../types";

defineProps<{ campaigns: Campaign[]; loading: boolean }>();
defineEmits<{ open: [campaign: Campaign] }>();

const phases = ["planning", "building", "reviewing", "implementation_complete"];

function phaseState(campaign: Campaign, phase: string): string {
  const order = phases.indexOf(phase);
  const current = phases.findIndex((candidate) => campaign.state.includes(candidate.replace("ing", "")));
  if (campaign.state === "implementation_complete") return "done";
  if (order < current) return "done";
  if (order === current) return "active";
  return "queued";
}
</script>

<template>
  <section class="campaign-page">
    <div class="page-heading">
      <div>
        <p class="kicker">LOCAL SOFTWARE FACTORY</p>
        <h1>Campaign sessions</h1>
      </div>
      <span class="count">{{ campaigns.length }} runs</span>
    </div>

    <div v-if="campaigns.length" class="campaign-grid">
      <button
        v-for="campaign in campaigns"
        :key="campaign.id"
        class="campaign-card"
        :class="{ running: isRunning(campaign.state), failed: campaign.state === 'failed' }"
        @click="$emit('open', campaign)"
      >
        <div class="card-top">
          <span class="campaign-id"><GitBranch :size="17" />{{ campaign.id }}</span>
          <span class="status-chip" :data-status="campaign.state">
            <i />{{ campaign.state.replaceAll("_", " ") }}
          </span>
        </div>
        <h2>{{ campaign.title }}</h2>
        <p class="profile">{{ campaign.profileId }}@{{ campaign.profileVersion }}</p>

        <div class="mini-trace" aria-label="Campaign phase progress">
          <span v-for="phase in phases" :key="phase" :class="phaseState(campaign, phase)">
            <i />
            <small>{{ phase.replaceAll("_", " ") }}</small>
          </span>
        </div>

        <div class="card-footer">
          <span><Activity :size="16" />{{ new Date(campaign.updatedAt).toLocaleString() }}</span>
          <span><Wrench :size="16" />{{ campaign.repairCycles }} repairs</span>
        </div>
      </button>
    </div>
    <div v-else class="empty-state">
      {{ loading ? "loading campaign sessions…" : "no campaign sessions yet" }}
    </div>
  </section>
</template>
