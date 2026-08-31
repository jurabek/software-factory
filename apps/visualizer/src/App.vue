<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { Factory, Radio } from "@lucide/vue";
import { api } from "./api";
import CampaignList from "./components/CampaignList.vue";
import TraceView from "./components/TraceView.vue";
import type { Campaign } from "./types";

const campaigns = ref<Campaign[]>([]);
const routeId = ref(decodeURIComponent(location.hash.replace(/^#\/?/, "")));
const loading = ref(true);
const error = ref("");
const connected = ref(false);
let timer: number | undefined;
let inflight = false;

const selected = computed(() => campaigns.value.find((campaign) => campaign.id === routeId.value) ?? null);

function syncRoute() {
  routeId.value = decodeURIComponent(location.hash.replace(/^#\/?/, ""));
}

function open(campaign: Campaign) {
  location.hash = `#/${encodeURIComponent(campaign.id)}`;
}

function back() {
  location.hash = "#/";
}

async function poll() {
  if (inflight) return;
  inflight = true;
  try {
    campaigns.value = await api.campaigns();
    connected.value = true;
    error.value = "";
  } catch (cause) {
    connected.value = false;
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    loading.value = false;
    inflight = false;
  }
}

onMounted(() => {
  window.addEventListener("hashchange", syncRoute);
  void poll();
  timer = window.setInterval(() => void poll(), 500);
});
onUnmounted(() => {
  window.removeEventListener("hashchange", syncRoute);
  window.clearInterval(timer);
});
</script>

<template>
  <header class="topbar">
    <button class="brand" @click="back">
      <span class="logo"><Factory :size="24" /></span>
      <span>Software Factory</span>
      <template v-if="selected">
        <b>›</b><span class="crumb">sessions</span><b>›</b><span class="crumb active">{{ selected.id }}</span>
      </template>
    </button>
    <span class="live-indicator" :class="{ offline: !connected }">
      <Radio :size="16" />{{ connected ? "live · WAL" : "reconnecting" }}
    </span>
  </header>

  <main>
    <TraceView v-if="selected" :key="selected.id" :campaign="selected" @back="back" />
    <CampaignList v-else :campaigns="campaigns" :loading="loading" @open="open" />
  </main>
  <p v-if="error && !selected" class="error-bar floating">{{ error }}</p>
</template>
