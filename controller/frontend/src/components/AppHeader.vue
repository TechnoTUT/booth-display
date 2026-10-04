<script setup lang="ts">
import {
  Activity,
  Sliders,
  Layers,
  Settings,
  Sun,
  Moon
} from 'lucide-vue-next'
import type { PipelineStatus } from '../types'

defineProps<{
  activeTab: 'monitor' | 'scenes' | 'canvas' | 'settings'
  pipeline: PipelineStatus
  isConnected: boolean
  totalBitrateMbps: string
  avgFps: string
  isDark: boolean
}>()

const emit = defineEmits<{
  (e: 'update:activeTab', tab: 'monitor' | 'scenes' | 'canvas' | 'settings'): void
  (e: 'toggleTheme'): void
}>()
</script>

<template>
  <header class="border-b border-slate-200 dark:border-slate-800 bg-white/90 dark:bg-slate-900/80 backdrop-blur px-4 sm:px-6 py-2.5 xl:py-3 flex flex-wrap xl:flex-nowrap items-center justify-between gap-y-2.5 sticky top-0 z-30 shadow-sm">
    <!-- 1st Row Left (Desktop: Left) -->
    <div class="flex items-center gap-5 order-1">
      <a href="/" class="flex items-center gap-3 sm:gap-4 py-0.5">
        <!-- Light theme logo -->
        <img src="/logo.svg" alt="TechnoTUT" class="h-9 sm:h-10 w-auto max-w-[140px] sm:max-w-[170px] object-contain dark:hidden" />
        <!-- Dark theme logo -->
        <img src="/logo_dark.svg" alt="TechnoTUT" class="h-9 sm:h-10 w-auto max-w-[140px] sm:max-w-[170px] object-contain hidden dark:block" />
        <div class="h-8 w-px bg-slate-200 dark:bg-slate-800"></div>
        <div>
          <h1 class="text-sm sm:text-base font-bold tracking-tight text-slate-900 dark:text-slate-100">
            Booth Display Controller
          </h1>
          <p class="text-[10px] sm:text-[11px] text-slate-500 dark:text-slate-400">The Utopia Tone Streaming Network</p>
        </div>
      </a>
    </div>

    <!-- 1st Row Right (Desktop: Far Right) -->
    <div class="flex items-center gap-2 sm:gap-3 order-2 xl:order-3">
      <!-- System Stats Badges (Hidden on mobile <768px, shown on tablet md: and desktop) -->
      <div class="hidden md:flex items-center gap-2 sm:gap-2.5 bg-slate-100 dark:bg-slate-900 px-3 py-1.5 rounded-xl border border-slate-200 dark:border-slate-800 text-xs font-mono select-none">
        <div class="flex items-center gap-1.5">
          <span class="text-slate-400 font-sans font-semibold text-[10px] uppercase">Stream</span>
          <span
            class="w-10 font-bold uppercase tracking-wider text-center"
            :class="pipeline.is_running ? 'text-emerald-500' : 'text-slate-400'"
          >
            {{ pipeline.is_running ? 'LIVE' : 'STOP' }}
          </span>
        </div>

        <div class="w-px h-3 bg-slate-300 dark:bg-slate-700"></div>

        <div class="flex items-center gap-1.5">
          <span class="text-slate-400 font-sans font-semibold text-[10px] uppercase">Rate</span>
          <span class="w-[78px] font-bold text-slate-700 dark:text-slate-200 tabular-nums text-right">
            {{ totalBitrateMbps }} Mbps
          </span>
        </div>

        <div class="w-px h-3 bg-slate-300 dark:bg-slate-700"></div>

        <div class="flex items-center gap-1.5">
          <span class="text-slate-400 font-sans font-semibold text-[10px] uppercase">FPS</span>
          <span class="w-11 font-bold text-slate-700 dark:text-slate-200 tabular-nums text-right">
            {{ avgFps }}
          </span>
        </div>
      </div>

      <!-- Connection Badge -->
      <div
        class="flex items-center gap-1.5 px-2.5 sm:px-3 py-1.5 rounded-xl border text-xs font-semibold"
        :class="isConnected
          ? 'border-emerald-200 dark:border-emerald-900 bg-emerald-50 dark:bg-emerald-950/40 text-emerald-600 dark:text-emerald-400'
          : 'border-rose-200 dark:border-rose-900 bg-rose-50 dark:bg-rose-950/40 text-rose-600 dark:text-rose-400'"
      >
        <span class="h-2 w-2 rounded-full" :class="isConnected ? 'bg-emerald-500 animate-pulse' : 'bg-rose-500'"></span>
        <span>{{ isConnected ? 'Online' : 'Offline' }}</span>
      </div>

      <!-- Dark/Light Theme Toggle -->
      <button
        @click="emit('toggleTheme')"
        class="p-2 rounded-xl border border-slate-200 dark:border-slate-800 text-slate-500 hover:text-slate-900 dark:text-slate-400 dark:hover:text-slate-100 hover:bg-slate-100 dark:hover:bg-slate-800 transition"
        title="Toggle Theme"
      >
        <Sun v-if="isDark" class="w-5 h-5 text-amber-400" />
        <Moon v-else class="w-5 h-5 text-slate-600" />
      </button>
    </div>

    <!-- 2nd Row (Tablet & Mobile) / Inline Middle-Right (Desktop) -->
    <div class="w-full xl:w-auto order-3 xl:order-2 xl:ml-auto xl:mr-3 flex items-center justify-end">
      <!-- Navigation Tabs -->
      <div class="flex w-full sm:w-auto bg-slate-100 dark:bg-slate-900 p-1 rounded-xl border border-slate-200 dark:border-slate-800">
        <button
          @click="emit('update:activeTab', 'monitor')"
          :class="[
            'flex-1 md:flex-initial px-3 sm:px-4 py-2 rounded-lg text-xs sm:text-sm font-semibold transition flex items-center justify-center gap-2',
            activeTab === 'monitor'
              ? 'bg-[#C7000A] text-white shadow-md shadow-[#C7000A]/20'
              : 'text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-slate-100'
          ]"
        >
          <Activity class="w-4 h-4" />
          <span>Control</span>
        </button>

        <button
          @click="emit('update:activeTab', 'scenes')"
          :class="[
            'flex-1 md:flex-initial px-3 sm:px-4 py-2 rounded-lg text-xs sm:text-sm font-semibold transition flex items-center justify-center gap-2',
            activeTab === 'scenes'
              ? 'bg-[#C7000A] text-white shadow-md shadow-[#C7000A]/20'
              : 'text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-slate-100'
          ]"
        >
          <Sliders class="w-4 h-4" />
          <span>Scenes</span>
        </button>

        <button
          @click="emit('update:activeTab', 'canvas')"
          :class="[
            'flex-1 md:flex-initial px-3 sm:px-4 py-2 rounded-lg text-xs sm:text-sm font-semibold transition flex items-center justify-center gap-2',
            activeTab === 'canvas'
              ? 'bg-[#C7000A] text-white shadow-md shadow-[#C7000A]/20'
              : 'text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-slate-100'
          ]"
        >
          <Layers class="w-4 h-4" />
          <span>Layout</span>
        </button>

        <button
          @click="emit('update:activeTab', 'settings')"
          :class="[
            'flex-1 md:flex-initial px-3 sm:px-4 py-2 rounded-lg text-xs sm:text-sm font-semibold transition flex items-center justify-center gap-2',
            activeTab === 'settings'
              ? 'bg-[#C7000A] text-white shadow-md shadow-[#C7000A]/20'
              : 'text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-slate-100'
          ]"
        >
          <Settings class="w-4 h-4" />
          <span>Config</span>
        </button>
      </div>
    </div>
  </header>
</template>
