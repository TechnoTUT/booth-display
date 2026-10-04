<script setup lang="ts">
import {
  Eye,
  EyeOff,
  RefreshCw,
  MonitorPlay,
  Play,
  Tv
} from 'lucide-vue-next'
import type { PipelineStatus, DisplayConfig } from '../types'

defineProps<{
  pipeline: PipelineStatus
  isActionLoading: boolean
  previewKey: number
  showCropOverlay: boolean
  canvasWidth: number
  canvasHeight: number
  canvasFps: number
  editableDisplays: DisplayConfig[]
}>()

const emit = defineEmits<{
  (e: 'play'): void
  (e: 'reloadPreview'): void
  (e: 'previewError'): void
  (e: 'toggleCropOverlay'): void
}>()
</script>

<template>
  <div class="space-y-6">
    <!-- Live Video Stream Preview (MJPEG) -->
    <div class="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-6 shadow-sm space-y-5">
      <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
        <div>
          <div class="flex items-center gap-2 mb-1">
            <span class="inline-block w-2.5 h-2.5 rounded-full" :class="pipeline.is_running ? 'bg-emerald-500 animate-pulse' : 'bg-slate-400'"></span>
            <h2 class="text-xs font-bold text-slate-500 dark:text-slate-400 uppercase tracking-wider">
              Live Master Canvas Feed
            </h2>
          </div>
          <p class="text-sm text-slate-600 dark:text-slate-300">
            Full virtual canvas video feed streamed via low-latency MJPEG with display crop regions.
          </p>
        </div>

        <!-- Preview Toolbar Controls -->
        <div class="flex items-center gap-2">
          <button
            @click="emit('toggleCropOverlay')"
            class="px-3 py-1.5 rounded-xl border text-xs font-medium flex items-center gap-1.5 transition cursor-pointer"
            :class="showCropOverlay ? 'bg-[#C7000A]/10 border-[#C7000A]/30 text-[#C7000A]' : 'bg-slate-100 dark:bg-slate-800 border-slate-200 dark:border-slate-700 text-slate-600 dark:text-slate-300'"
            :title="showCropOverlay ? 'Hide Panel Crop Overlay' : 'Show Panel Crop Overlay'"
          >
            <Eye v-if="showCropOverlay" class="w-3.5 h-3.5" />
            <EyeOff v-else class="w-3.5 h-3.5" />
            <span>Crop Overlay</span>
          </button>

          <button
            @click="emit('reloadPreview')"
            class="px-3 py-1.5 rounded-xl bg-slate-100 dark:bg-slate-800 hover:bg-slate-200 dark:hover:bg-slate-700 border border-slate-200 dark:border-slate-700 text-xs font-medium text-slate-700 dark:text-slate-200 flex items-center gap-1.5 transition cursor-pointer"
            title="Reload Preview Stream"
          >
            <RefreshCw class="w-3.5 h-3.5" />
            <span>Reload</span>
          </button>
        </div>
      </div>

      <!-- Video Player Container with Adaptive Aspect Ratio -->
      <div
        class="relative w-full bg-slate-950 rounded-2xl overflow-hidden border border-slate-200 dark:border-slate-800/80 shadow-inner flex items-center justify-center select-none"
        :style="{ aspectRatio: `${canvasWidth} / ${canvasHeight}` }"
      >
        <!-- Live MJPEG Stream Image -->
        <img
          v-if="pipeline.is_running"
          :src="`/api/preview/mjpeg?t=${previewKey}`"
          @error="emit('previewError')"
          alt="Live Canvas Preview"
          class="w-full h-full object-contain pointer-events-none"
        />

        <!-- Offline / Standby State -->
        <div v-else class="absolute inset-0 flex flex-col items-center justify-center p-6 text-center space-y-4 bg-slate-900/90 backdrop-blur-sm">
          <div class="p-3.5 rounded-2xl bg-slate-800/80 text-slate-400 border border-slate-700/60 shadow-lg">
            <MonitorPlay class="w-8 h-8 text-slate-400" />
          </div>
          <div class="space-y-1">
            <h3 class="text-base font-bold text-slate-200">Stream Offline</h3>
            <p class="text-xs text-slate-400 max-w-md">
              The encoder pipeline is currently stopped. Start the streaming pipeline to view the synchronized multi-panel canvas output.
            </p>
          </div>
          <button
            @click="emit('play')"
            :disabled="isActionLoading"
            class="bg-[#C7000A] hover:bg-[#b00009] disabled:opacity-50 text-white font-semibold py-2 px-5 rounded-xl shadow-lg shadow-[#C7000A]/30 transition flex items-center gap-2 text-xs cursor-pointer"
          >
            <Play class="w-3.5 h-3.5 fill-current" />
            <span>Start Stream</span>
          </button>
        </div>

        <!-- Panel Boundary & Crop Overlays -->
        <div
          v-if="showCropOverlay && canvasWidth > 0 && canvasHeight > 0"
          class="absolute inset-0 pointer-events-none"
        >
          <template v-for="(disp, idx) in editableDisplays" :key="disp.id">
            <div
              class="absolute top-0 bottom-0 border-2 border-emerald-400/80 dark:border-emerald-400/90 bg-emerald-500/10 transition-all flex flex-col justify-between p-2"
              :style="{
                left: `${(disp.crop_x / canvasWidth) * 100}%`,
                width: `${(disp.width / canvasWidth) * 100}%`
              }"
            >
              <div class="flex items-center justify-between">
                <span class="text-[10px] font-bold font-mono px-1.5 py-0.5 rounded bg-emerald-950/80 text-emerald-300 border border-emerald-700/60 shadow-sm backdrop-blur">
                  #{{ idx + 1 }} {{ disp.name }}
                </span>
                <span class="text-[9px] font-mono px-1 rounded bg-black/60 text-slate-300">
                  {{ disp.width }}×{{ disp.height }}
                </span>
              </div>
              <div class="flex items-center justify-between text-[9px] font-mono text-emerald-300/80">
                <span class="bg-black/50 px-1 rounded">X: {{ disp.crop_x }}</span>
                <span v-if="disp.bezel_padding_right > 0" class="text-amber-300 bg-black/60 px-1 rounded">
                  +{{ disp.bezel_padding_right }}px bezel
                </span>
              </div>
            </div>
          </template>
        </div>
      </div>
    </div>

    <!-- Virtual Canvas Topology Preview -->
    <div class="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-6 shadow-sm space-y-5">
      <div class="flex flex-col md:flex-row md:items-center justify-between gap-3">
        <div>
          <h2 class="text-xs font-bold text-slate-500 dark:text-slate-400 uppercase tracking-wider mb-1">
            Virtual Canvas Topology Preview
          </h2>
          <p class="text-sm text-slate-600 dark:text-slate-300">
            Rendered as a single virtual canvas on the controller, cropped and split to individual display feeds.
          </p>
        </div>
        <div class="font-mono text-xs px-3.5 py-1.5 rounded-xl bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300 border border-slate-200 dark:border-slate-700 flex items-center gap-1.5 select-none">
          <span class="text-slate-400">Total Canvas:</span>
          <strong class="text-[#C7000A]">{{ canvasWidth }}</strong>
          <span class="text-slate-400">×</span>
          <strong class="text-slate-900 dark:text-slate-100">{{ canvasHeight }}</strong>
          <span class="text-slate-400">px @</span>
          <strong class="text-slate-900 dark:text-slate-100">{{ canvasFps }}</strong>
          <span class="text-slate-400">fps</span>
        </div>
      </div>

      <!-- Ultra-wide Multi-Panel Visualizer (Adaptive Theme) -->
      <div class="p-6 bg-slate-100 dark:bg-slate-950 rounded-2xl border border-slate-200 dark:border-slate-800/80 overflow-x-auto transition-colors">
        <div class="flex items-stretch min-w-[700px] h-[170px] gap-2.5 select-none">
          <template v-for="(disp, idx) in editableDisplays" :key="disp.id">
            
            <!-- Display Slice -->
            <div class="flex-1 bg-white dark:bg-slate-900 rounded-xl border-2 border-slate-300 dark:border-slate-700 hover:border-[#C7000A] dark:hover:border-[#C7000A] shadow-sm transition p-3.5 flex flex-col justify-between relative overflow-hidden group">
              <div class="flex justify-between items-start">
                <span class="font-bold text-xs text-slate-800 dark:text-slate-100 flex items-center gap-1.5">
                  <Tv class="w-3.5 h-3.5 text-[#C7000A]" />
                  {{ disp.name }}
                </span>
                <span class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-slate-100 dark:bg-slate-800 text-slate-600 dark:text-slate-300 border border-slate-200 dark:border-slate-700">
                  {{ disp.width }}×{{ disp.height }}
                </span>
              </div>

              <div class="text-center py-2">
                <span class="text-2xl font-black tracking-widest text-slate-300 dark:text-slate-700 group-hover:text-[#C7000A] dark:group-hover:text-slate-500 transition">
                  #{{ idx + 1 }}
                </span>
              </div>

              <div class="flex justify-between items-center text-[11px] font-mono text-slate-500 dark:text-slate-400 border-t border-slate-100 dark:border-slate-800/80 pt-2">
                <span>Crop: ({{ disp.crop_x }}, {{ disp.crop_y }})</span>
                <span class="text-[10px] text-slate-400">#{{ idx + 1 }}</span>
              </div>
            </div>

            <!-- Bezel Gap Visualizer -->
            <div
              v-if="disp.bezel_padding_right > 0 && idx < editableDisplays.length - 1"
              class="w-7 bg-slate-200/90 dark:bg-slate-900 border border-dashed border-slate-300 dark:border-slate-700 rounded-lg flex flex-col items-center justify-center text-[10px] font-mono text-slate-500 dark:text-slate-400 py-1"
              :title="`Bezel Gap: ${disp.bezel_padding_right}px`"
            >
              <span class="rotate-90 whitespace-nowrap">{{ disp.bezel_padding_right }}px</span>
            </div>
          </template>
        </div>
      </div>

      <div class="p-3.5 bg-slate-50 dark:bg-slate-950/60 rounded-xl border border-slate-200 dark:border-slate-800/80 text-xs text-slate-600 dark:text-slate-400 flex items-center gap-2.5">
        <span class="inline-block w-2 h-2 rounded-full bg-[#C7000A]"></span>
        <span>Bezel padding pixels insert hidden spacer areas between panels to eliminate geometric distortion across bezels.</span>
      </div>
    </div>
  </div>
</template>
