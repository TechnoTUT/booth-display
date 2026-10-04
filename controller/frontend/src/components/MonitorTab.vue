<script setup lang="ts">
import {
  Play,
  Square,
  Radio,
  RefreshCw,
  Tv
} from 'lucide-vue-next'
import type { PipelineStatus, NDISourceItem, DisplayMetrics } from '../types'

defineProps<{
  pipeline: PipelineStatus
  isActionLoading: boolean
  selectedMode: 'testpattern' | 'video' | 'ndi'
  videoFilePath: string
  selectedNdiSource: string
  ndiSources: NDISourceItem[]
  isNdiScanning: boolean
  sortedMetrics: DisplayMetrics[]
}>()

const emit = defineEmits<{
  (e: 'play'): void
  (e: 'stop'): void
  (e: 'fetchNdiSources'): void
  (e: 'update:selectedMode', val: 'testpattern' | 'video' | 'ndi'): void
  (e: 'update:videoFilePath', val: string): void
  (e: 'update:selectedNdiSource', val: string): void
}>()
</script>

<template>
  <div class="space-y-6">
    <!-- Playback Control Card -->
    <div class="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-6 shadow-sm">
      <div class="flex flex-col md:flex-row md:items-center justify-between gap-4">
        <div>
          <h2 class="text-xs font-bold text-slate-500 dark:text-slate-400 uppercase tracking-wider mb-1">
            Streaming Control
          </h2>
          <p class="text-sm text-slate-600 dark:text-slate-300">
            Synchronized low-latency UDP (H.264 Annex-B) streaming across all connected panels.
          </p>
        </div>

        <div class="flex items-center gap-3">
          <button
            @click="emit('play')"
            :disabled="pipeline.is_running || isActionLoading"
            class="bg-[#C7000A] hover:bg-[#b00009] disabled:opacity-50 text-white font-semibold py-2.5 px-6 rounded-xl shadow-md shadow-[#C7000A]/20 transition flex items-center gap-2 text-sm cursor-pointer"
          >
            <Play class="w-4 h-4 fill-current" />
            <span>Start Stream</span>
          </button>

          <button
            @click="emit('stop')"
            :disabled="!pipeline.is_running || isActionLoading"
            class="bg-slate-800 hover:bg-slate-700 disabled:opacity-40 text-slate-100 font-semibold py-2.5 px-5 rounded-xl border border-slate-700 transition flex items-center gap-2 text-sm cursor-pointer"
          >
            <Square class="w-4 h-4 fill-current text-rose-500" />
            <span>Stop Stream</span>
          </button>
        </div>
      </div>

      <!-- Source Selector Row -->
      <div class="mt-6 pt-5 border-t border-slate-100 dark:border-slate-800/80 flex flex-wrap items-center gap-4">
        <div class="flex items-center gap-2">
          <Radio class="w-4 h-4 text-[#C7000A]" />
          <label class="text-sm font-semibold text-slate-700 dark:text-slate-300">Source:</label>
          <select
            :value="selectedMode"
            @change="emit('update:selectedMode', ($event.target as HTMLSelectElement).value as any)"
            :disabled="pipeline.is_running"
            class="bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-xl px-3.5 py-2 text-sm font-medium text-slate-800 dark:text-slate-100 focus:outline-none focus:ring-2 focus:ring-[#C7000A]"
          >
            <option value="testpattern">Internal Test Pattern (Multi-Screen Sync)</option>
            <option value="video">Local Video File (MP4 / H.264)</option>
            <option value="ndi">NDI Video Source (Network Stream)</option>
          </select>
        </div>

        <!-- Video File Input -->
        <div v-if="selectedMode === 'video'" class="flex-1 flex items-center gap-2 min-w-[280px]">
          <input
            type="text"
            :value="videoFilePath"
            @input="emit('update:videoFilePath', ($event.target as HTMLInputElement).value)"
            :disabled="pipeline.is_running"
            placeholder="/path/to/multi_screen_video.mp4"
            class="flex-1 bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-xl px-3.5 py-2 text-sm font-mono text-slate-800 dark:text-slate-100 focus:outline-none focus:ring-2 focus:ring-[#C7000A]"
          />
        </div>

        <!-- NDI Source Selector -->
        <div v-if="selectedMode === 'ndi'" class="flex-1 flex items-center gap-2 min-w-[280px]">
          <select
            :value="selectedNdiSource"
            @change="emit('update:selectedNdiSource', ($event.target as HTMLSelectElement).value)"
            :disabled="pipeline.is_running"
            class="flex-1 bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-xl px-3.5 py-2 text-sm font-mono text-slate-800 dark:text-slate-100 focus:outline-none focus:ring-2 focus:ring-[#C7000A]"
          >
            <option value="" disabled>-- Select an NDI Source --</option>
            <option v-for="src in ndiSources" :key="src.name" :value="src.name">
              {{ src.name }}
            </option>
          </select>

          <button
            @click="emit('fetchNdiSources')"
            :disabled="pipeline.is_running || isNdiScanning"
            class="px-3.5 py-2 rounded-xl bg-slate-100 dark:bg-slate-800 hover:bg-slate-200 dark:hover:bg-slate-700 border border-slate-200 dark:border-slate-700 text-xs font-semibold text-slate-700 dark:text-slate-200 flex items-center gap-1.5 transition cursor-pointer"
            title="Scan for NDI sources"
          >
            <RefreshCw class="w-3.5 h-3.5" :class="{ 'animate-spin': isNdiScanning }" />
            <span>{{ isNdiScanning ? 'Scanning...' : 'Scan NDI' }}</span>
          </button>
        </div>
      </div>
    </div>

    <!-- Real-time Display Status Grid -->
    <div>
      <div class="flex items-center justify-between mb-3 px-1">
        <h2 class="text-xs font-bold text-slate-500 dark:text-slate-400 uppercase tracking-wider">
          Connected Panels ({{ sortedMetrics.length }})
        </h2>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
        <div
          v-for="m in sortedMetrics"
          :key="m.id"
          class="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-5 shadow-sm space-y-4 hover:border-slate-300 dark:hover:border-slate-700 transition"
        >
          <!-- Card Header -->
          <div class="flex items-center justify-between border-b border-slate-100 dark:border-slate-800 pb-3">
            <div class="flex items-center gap-2.5">
              <div class="p-2 rounded-xl bg-slate-100 dark:bg-slate-800 text-slate-600 dark:text-slate-300">
                <Tv class="w-4 h-4 text-[#C7000A]" />
              </div>
              <div>
                <h3 class="font-bold text-sm text-slate-900 dark:text-slate-100">{{ m.name }}</h3>
                <p class="text-[11px] font-mono text-slate-500 dark:text-slate-400">{{ m.id }}</p>
              </div>
            </div>
            <span class="px-2.5 py-1 rounded-full text-xs font-mono font-semibold bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300">
              {{ m.target }}
            </span>
          </div>

          <!-- Metric Rows -->
          <div class="space-y-2 text-xs">
            <div class="flex justify-between items-center">
              <span class="text-slate-500 dark:text-slate-400">Render Rate:</span>
              <span class="font-mono font-bold text-slate-900 dark:text-slate-100 tabular-nums">
                {{ m.fps.toFixed(1) }} fps
              </span>
            </div>

            <div class="flex justify-between items-center">
              <span class="text-slate-500 dark:text-slate-400">Bitrate:</span>
              <span class="font-mono font-bold text-sky-600 dark:text-sky-400 tabular-nums">
                {{ (m.bitrate_kbps / 1000).toFixed(2) }} Mbps ({{ m.bitrate_kbps.toFixed(0) }} kbps)
              </span>
            </div>

            <div class="flex justify-between items-center">
              <span class="text-slate-500 dark:text-slate-400">Frames Sent:</span>
              <span class="font-mono text-slate-700 dark:text-slate-300 tabular-nums">
                {{ m.frames_sent.toLocaleString() }}
              </span>
            </div>

            <div class="flex justify-between items-center">
              <span class="text-slate-500 dark:text-slate-400">Packets Sent:</span>
              <span class="font-mono text-slate-700 dark:text-slate-300 tabular-nums">
                {{ m.packets_sent.toLocaleString() }}
              </span>
            </div>

            <div class="flex justify-between items-center pt-2 border-t border-slate-100 dark:border-slate-800/80">
              <span class="text-slate-500 dark:text-slate-400">Packet Drops:</span>
              <span
                class="font-mono font-bold px-2 py-0.5 rounded text-[11px]"
                :class="m.drops > 0 ? 'bg-rose-100 dark:bg-rose-950/60 text-rose-600' : 'text-emerald-500'"
              >
                {{ m.drops }}
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
