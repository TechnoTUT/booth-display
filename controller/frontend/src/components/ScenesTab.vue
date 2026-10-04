<script setup lang="ts">
import { ref, computed } from 'vue'
import {
  Tv,
  Zap,
  Sparkles,
  RefreshCw,
  Sliders,
  Video,
  Layers,
  CircleDot,
  Loader2
} from 'lucide-vue-next'
import type { SceneStatus, NDISourceItem, PipelineStatus, AssetFile } from '../types'

const props = defineProps<{
  scene: SceneStatus
  pipeline: PipelineStatus
  ndiSources: NDISourceItem[]
  isNdiScanning: boolean
  videoFilePath: string
  previewKey: number
  assetFiles?: AssetFile[]
}>()

const emit = defineEmits<{
  (e: 'refreshNdi'): void
  (e: 'switchScene', payload: { sourceType: string; target: string; transition: string; durationMs: number }): void
  (e: 'reloadPreview'): void
}>()

// Transition controls
const selectedTransition = ref<'cut' | 'fade' | 'black'>('fade')
const transitionDurationMs = ref<number>(500)
const isSwitching = ref(false)

const durationPresets = [200, 500, 1000, 1500]

function setDuration(val: number) {
  transitionDurationMs.value = val
}

async function handleTake(sourceType: string, target: string = '') {
  isSwitching.value = true
  try {
    emit('switchScene', {
      sourceType,
      target,
      transition: selectedTransition.value,
      durationMs: transitionDurationMs.value
    })
  } finally {
    setTimeout(() => {
      isSwitching.value = false
    }, 300)
  }
}

function isCurrentLive(sourceType: string, target: string = '') {
  if (sourceType === 'testpattern') {
    return props.scene.active_source === 'testpattern'
  }
  if (sourceType === 'rainbow') {
    return props.scene.active_source === 'rainbow'
  }
  if (sourceType === 'logo') {
    return props.scene.active_source === 'logo'
  }
  if (sourceType === 'video') {
    if (target) {
      return props.scene.active_source === 'video' && props.scene.active_target === target
    }
    return props.scene.active_source === 'video'
  }
  if (props.scene.active_source !== sourceType) return false
  if (sourceType === 'ndi') {
    return props.scene.active_target === target
  }
  return true
}

const activeSourceLabel = computed(() => {
  const src = props.scene.active_source
  if (src === 'ndi') {
    return `NDI: ${props.scene.active_target || 'Default'}`
  } else if (src === 'video') {
    return `Video: ${props.scene.active_target || props.videoFilePath || 'Local File'}`
  } else if (src === 'logo') {
    return 'TechnoTUT Logo'
  } else if (src === 'rainbow') {
    return 'Rainbow Motion (FFmpeg testsrc)'
  } else if (src === 'testpattern') {
    return 'SMPTE Color Bars (Grid & Guide)'
  }
  return src || 'None'
})
</script>

<template>
  <div class="space-y-6">
    <!-- Top Row: Live Monitor & Active Scene Info -->
    <div class="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-5 shadow-sm">
      <div class="flex flex-col lg:flex-row lg:items-center justify-between gap-4 mb-4">
        <div class="flex items-center gap-3">
          <div class="p-2.5 rounded-xl bg-red-500/10 text-red-500">
            <Tv class="w-6 h-6" />
          </div>
          <div>
            <div class="flex items-center gap-2">
              <h2 class="text-base font-bold text-slate-900 dark:text-slate-100">Live Canvas Output</h2>
              <span
                class="px-2.5 py-0.5 rounded-full text-xs font-bold tracking-wider uppercase"
                :class="pipeline.is_running ? 'bg-red-500 text-white animate-pulse' : 'bg-slate-200 dark:bg-slate-700 text-slate-600 dark:text-slate-400'"
              >
                {{ pipeline.is_running ? 'LIVE' : 'STANDBY' }}
              </span>
            </div>
            <p class="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
              Active: <span class="font-semibold text-slate-800 dark:text-slate-200">{{ activeSourceLabel }}</span>
            </p>
          </div>
        </div>

        <!-- Transition Progress Indicator -->
        <div v-if="scene.in_transition" class="flex items-center gap-3 bg-amber-50 dark:bg-amber-950/40 border border-amber-200 dark:border-amber-900/60 px-4 py-2 rounded-xl text-amber-700 dark:text-amber-300 text-xs">
          <Sparkles class="w-4 h-4 animate-spin text-amber-500" />
          <div>
            <div class="font-semibold uppercase tracking-wider text-[10px]">Transitioning ({{ scene.transition }})</div>
            <div class="w-32 bg-amber-200 dark:bg-amber-900 h-1.5 rounded-full overflow-hidden mt-1">
              <div
                class="bg-amber-500 h-full transition-all duration-75"
                :style="{ width: `${Math.round((scene.progress || 0) * 100)}%` }"
              ></div>
            </div>
          </div>
        </div>
      </div>

      <!-- Preview Image Stream -->
      <div class="relative rounded-xl overflow-hidden bg-slate-950 border border-slate-800 flex items-center justify-center min-h-[140px]">
        <img
          v-if="pipeline.is_running"
          :src="`/api/preview/mjpeg?t=${previewKey}`"
          alt="Live Stream Preview"
          class="w-full h-auto object-contain max-h-[220px]"
          @error="emit('reloadPreview')"
        />
        <div v-else class="text-center py-12 text-slate-500 text-sm flex flex-col items-center gap-2">
          <Tv class="w-8 h-8 opacity-40" />
          <span>Stream is currently stopped</span>
        </div>
      </div>
    </div>

    <!-- Middle: Transition Controls & Effects -->
    <div class="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-5 shadow-sm">
      <div class="flex items-center gap-2.5 mb-4 pb-3 border-b border-slate-100 dark:border-slate-800">
        <Sliders class="w-5 h-5 text-indigo-500" />
        <h3 class="text-sm font-bold text-slate-900 dark:text-slate-100">Transition Settings</h3>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
        <!-- Transition Style -->
        <div>
          <label class="block text-xs font-semibold text-slate-600 dark:text-slate-400 mb-2">Effect Style</label>
          <div class="grid grid-cols-3 gap-2.5">
            <button
              @click="selectedTransition = 'cut'"
              :class="[
                'flex flex-col items-center gap-1.5 p-3 rounded-xl border text-xs font-semibold transition',
                selectedTransition === 'cut'
                  ? 'border-indigo-500 bg-indigo-50 dark:bg-indigo-950/40 text-indigo-600 dark:text-indigo-400 shadow-sm'
                  : 'border-slate-200 dark:border-slate-800 hover:bg-slate-50 dark:hover:bg-slate-800 text-slate-700 dark:text-slate-300'
              ]"
            >
              <Zap class="w-5 h-5" />
              <span>CUT (Instant)</span>
            </button>

            <button
              @click="selectedTransition = 'fade'"
              :class="[
                'flex flex-col items-center gap-1.5 p-3 rounded-xl border text-xs font-semibold transition',
                selectedTransition === 'fade'
                  ? 'border-indigo-500 bg-indigo-50 dark:bg-indigo-950/40 text-indigo-600 dark:text-indigo-400 shadow-sm'
                  : 'border-slate-200 dark:border-slate-800 hover:bg-slate-50 dark:hover:bg-slate-800 text-slate-700 dark:text-slate-300'
              ]"
            >
              <Sparkles class="w-5 h-5" />
              <span>CROSSFADE</span>
            </button>

            <button
              @click="selectedTransition = 'black'"
              :class="[
                'flex flex-col items-center gap-1.5 p-3 rounded-xl border text-xs font-semibold transition',
                selectedTransition === 'black'
                  ? 'border-indigo-500 bg-indigo-50 dark:bg-indigo-950/40 text-indigo-600 dark:text-indigo-400 shadow-sm'
                  : 'border-slate-200 dark:border-slate-800 hover:bg-slate-50 dark:hover:bg-slate-800 text-slate-700 dark:text-slate-300'
              ]"
            >
              <CircleDot class="w-5 h-5" />
              <span>DIP TO BLACK</span>
            </button>
          </div>
        </div>

        <!-- Duration Settings (Disabled when CUT is selected) -->
        <div :class="{ 'opacity-40 pointer-events-none': selectedTransition === 'cut' }">
          <div class="flex items-center justify-between mb-2">
            <label class="text-xs font-semibold text-slate-600 dark:text-slate-400">Duration</label>
            <span class="text-xs font-mono font-bold text-slate-900 dark:text-slate-100">
              {{ (transitionDurationMs / 1000).toFixed(2) }}s ({{ transitionDurationMs }}ms)
            </span>
          </div>

          <!-- Presets -->
          <div class="flex items-center gap-2 mb-3">
            <button
              v-for="d in durationPresets"
              :key="d"
              @click="setDuration(d)"
              :class="[
                'px-2.5 py-1 rounded-lg text-xs font-medium border transition',
                transitionDurationMs === d
                  ? 'bg-slate-800 text-white dark:bg-slate-200 dark:text-slate-900 border-transparent'
                  : 'border-slate-200 dark:border-slate-800 text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800'
              ]"
            >
              {{ d }}ms
            </button>
          </div>

          <input
            type="range"
            min="100"
            max="2000"
            step="50"
            v-model.number="transitionDurationMs"
            class="w-full accent-indigo-600 cursor-pointer"
          />
        </div>
      </div>
    </div>

    <!-- Bottom: Quick Scene Switcher Buttons -->
    <div class="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-5 shadow-sm">
      <div class="flex items-center justify-between mb-4 pb-3 border-b border-slate-100 dark:border-slate-800">
        <div class="flex items-center gap-2.5">
          <Layers class="w-5 h-5 text-emerald-500" />
          <div>
            <h3 class="text-sm font-bold text-slate-900 dark:text-slate-100">Quick Switcher (Press to TAKE)</h3>
            <p class="text-xs text-slate-500 dark:text-slate-400">Clicking any scene tile switches live feed immediately using selected transition.</p>
          </div>
        </div>

        <button
          @click="emit('refreshNdi')"
          :disabled="isNdiScanning"
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-xl border border-slate-200 dark:border-slate-800 text-xs font-medium text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800 transition disabled:opacity-50"
        >
          <Loader2 v-if="isNdiScanning" class="w-3.5 h-3.5 animate-spin" />
          <RefreshCw v-else class="w-3.5 h-3.5" />
          <span>Scan NDI</span>
        </button>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
        <!-- 1. SMPTE Color Bars Tile -->
        <div
          @click="handleTake('testpattern', 'bars')"
          :class="[
            'relative p-4 rounded-xl border-2 transition cursor-pointer select-none flex flex-col justify-between h-32 group',
            isCurrentLive('testpattern')
              ? 'border-red-500 bg-red-50/40 dark:bg-red-950/20 shadow-md shadow-red-500/10'
              : 'border-slate-200 dark:border-slate-800 hover:border-slate-300 dark:hover:border-slate-700 bg-slate-50/50 dark:bg-slate-900/50'
          ]"
        >
          <div class="flex items-start justify-between">
            <div class="flex items-center gap-2.5">
              <div class="p-2 rounded-lg bg-amber-500/10 text-amber-600 dark:text-amber-400">
                <Sliders class="w-5 h-5" />
              </div>
              <div>
                <h4 class="text-sm font-bold text-slate-900 dark:text-slate-100">Color Bars</h4>
                <p class="text-[11px] text-slate-500 dark:text-slate-400">SMPTE & Boundary Guide</p>
              </div>
            </div>
            <span
              v-if="isCurrentLive('testpattern')"
              class="px-2 py-0.5 rounded-full text-[10px] font-bold bg-red-500 text-white uppercase tracking-wider"
            >
              LIVE
            </span>
          </div>

          <div class="flex items-center justify-between text-xs text-slate-500">
            <span>Built-in Generator</span>
            <span class="font-semibold text-indigo-600 dark:text-indigo-400 group-hover:underline">TAKE &rarr;</span>
          </div>
        </div>

        <!-- 2. Rainbow Motion (FFmpeg testsrc) Tile -->
        <div
          @click="handleTake('rainbow', 'testsrc')"
          :class="[
            'relative p-4 rounded-xl border-2 transition cursor-pointer select-none flex flex-col justify-between h-32 group',
            isCurrentLive('rainbow')
              ? 'border-red-500 bg-red-50/40 dark:bg-red-950/20 shadow-md shadow-red-500/10'
              : 'border-slate-200 dark:border-slate-800 hover:border-slate-300 dark:hover:border-slate-700 bg-slate-50/50 dark:bg-slate-900/50'
          ]"
        >
          <div class="flex items-start justify-between">
            <div class="flex items-center gap-2.5">
              <div class="p-2 rounded-lg bg-purple-500/10 text-purple-600 dark:text-purple-400">
                <Sparkles class="w-5 h-5" />
              </div>
              <div>
                <h4 class="text-sm font-bold text-slate-900 dark:text-slate-100">Rainbow Motion</h4>
                <p class="text-[11px] text-slate-500 dark:text-slate-400">Dynamic Multi-Color Pattern</p>
              </div>
            </div>
            <span
              v-if="isCurrentLive('rainbow')"
              class="px-2 py-0.5 rounded-full text-[10px] font-bold bg-red-500 text-white uppercase tracking-wider"
            >
              LIVE
            </span>
          </div>

          <div class="flex items-center justify-between text-xs text-slate-500">
            <span>Dynamic testsrc</span>
            <span class="font-semibold text-indigo-600 dark:text-indigo-400 group-hover:underline">TAKE &rarr;</span>
          </div>
        </div>

        <!-- 3. Logo Animation Tile -->
        <div
          @click="handleTake('logo', '')"
          :class="[
            'relative p-4 rounded-xl border-2 transition cursor-pointer select-none flex flex-col justify-between h-32 group',
            isCurrentLive('logo')
              ? 'border-red-500 bg-red-50/40 dark:bg-red-950/20 shadow-md shadow-red-500/10'
              : 'border-slate-200 dark:border-slate-800 hover:border-slate-300 dark:hover:border-slate-700 bg-slate-50/50 dark:bg-slate-900/50'
          ]"
        >
          <div class="flex items-start justify-between">
            <div class="flex items-center gap-2.5">
              <div class="p-2 rounded-lg bg-red-500/10 text-red-600 dark:text-red-400">
                <Tv class="w-5 h-5" />
              </div>
              <div>
                <h4 class="text-sm font-bold text-slate-900 dark:text-slate-100">TechnoTUT Logo</h4>
                <p class="text-[11px] text-slate-500 dark:text-slate-400">Continuous Tiled Crawl (Right to Left)</p>
              </div>
            </div>
            <span
              v-if="isCurrentLive('logo')"
              class="px-2 py-0.5 rounded-full text-[10px] font-bold bg-red-500 text-white uppercase tracking-wider"
            >
              LIVE
            </span>
          </div>

          <div class="flex items-center justify-between text-xs text-slate-500">
            <span>Speed Motion</span>
            <span class="font-semibold text-indigo-600 dark:text-indigo-400 group-hover:underline">TAKE &rarr;</span>
          </div>
        </div>

        <!-- 2. Video File Tile -->
        <div
          v-if="videoFilePath"
          @click="handleTake('video', videoFilePath)"
          :class="[
            'relative p-4 rounded-xl border-2 transition cursor-pointer select-none flex flex-col justify-between h-32 group',
            isCurrentLive('video')
              ? 'border-red-500 bg-red-50/40 dark:bg-red-950/20 shadow-md shadow-red-500/10'
              : 'border-slate-200 dark:border-slate-800 hover:border-slate-300 dark:hover:border-slate-700 bg-slate-50/50 dark:bg-slate-900/50'
          ]"
        >
          <div class="flex items-start justify-between">
            <div class="flex items-center gap-2.5">
              <div class="p-2 rounded-lg bg-blue-500/10 text-blue-600 dark:text-blue-400">
                <Video class="w-5 h-5" />
              </div>
              <div class="overflow-hidden">
                <h4 class="text-sm font-bold text-slate-900 dark:text-slate-100 truncate">Video File</h4>
                <p class="text-[11px] text-slate-500 dark:text-slate-400 truncate max-w-[170px]" :title="videoFilePath">
                  {{ videoFilePath }}
                </p>
              </div>
            </div>
            <span
              v-if="isCurrentLive('video')"
              class="px-2 py-0.5 rounded-full text-[10px] font-bold bg-red-500 text-white uppercase tracking-wider"
            >
              LIVE
            </span>
          </div>

          <div class="flex items-center justify-between text-xs text-slate-500">
            <span>Loop Playback</span>
            <span class="font-semibold text-indigo-600 dark:text-indigo-400 group-hover:underline">TAKE &rarr;</span>
          </div>
        </div>

        <!-- 4. User Assets from /assets directory -->
        <div
          v-for="asset in (assetFiles || [])"
          :key="asset.path"
          @click="handleTake('video', asset.path)"
          :class="[
            'relative p-4 rounded-xl border-2 transition cursor-pointer select-none flex flex-col justify-between h-32 group',
            isCurrentLive('video', asset.path)
              ? 'border-red-500 bg-red-50/40 dark:bg-red-950/20 shadow-md shadow-red-500/10'
              : 'border-slate-200 dark:border-slate-800 hover:border-slate-300 dark:hover:border-slate-700 bg-slate-50/50 dark:bg-slate-900/50'
          ]"
        >
          <div class="flex items-start justify-between">
            <div class="flex items-center gap-2.5 overflow-hidden">
              <div class="p-2 rounded-lg bg-teal-500/10 text-teal-600 dark:text-teal-400 shrink-0">
                <Video class="w-5 h-5" />
              </div>
              <div class="overflow-hidden">
                <h4 class="text-sm font-bold text-slate-900 dark:text-slate-100 truncate" :title="asset.name">
                  {{ asset.name }}
                </h4>
                <p class="text-[11px] text-slate-500 dark:text-slate-400 truncate">
                  /{{ asset.path }}
                </p>
              </div>
            </div>
            <span
              v-if="isCurrentLive('video', asset.path)"
              class="px-2 py-0.5 rounded-full text-[10px] font-bold bg-red-500 text-white uppercase tracking-wider shrink-0"
            >
              LIVE
            </span>
          </div>

          <div class="flex items-center justify-between text-xs text-slate-500">
            <span class="text-[11px] text-teal-600 dark:text-teal-400 font-semibold">User Asset</span>
            <span class="font-semibold text-indigo-600 dark:text-indigo-400 group-hover:underline">TAKE &rarr;</span>
          </div>
        </div>

        <!-- 3. NDI Sources Tiles -->
        <div
          v-for="src in ndiSources"
          :key="src.name"
          @click="handleTake('ndi', src.name)"
          :class="[
            'relative p-4 rounded-xl border-2 transition cursor-pointer select-none flex flex-col justify-between h-32 group',
            isCurrentLive('ndi', src.name)
              ? 'border-red-500 bg-red-50/40 dark:bg-red-950/20 shadow-md shadow-red-500/10'
              : 'border-slate-200 dark:border-slate-800 hover:border-slate-300 dark:hover:border-slate-700 bg-slate-50/50 dark:bg-slate-900/50'
          ]"
        >
          <div class="flex items-start justify-between">
            <div class="flex items-center gap-2.5 overflow-hidden">
              <div class="p-2 rounded-lg bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 shrink-0">
                <Tv class="w-5 h-5" />
              </div>
              <div class="overflow-hidden">
                <h4 class="text-sm font-bold text-slate-900 dark:text-slate-100 truncate" :title="src.name">
                  {{ src.stream_name || src.name }}
                </h4>
                <p class="text-[11px] text-slate-500 dark:text-slate-400 truncate">
                  Host: {{ src.host_name || 'Network Source' }}
                </p>
              </div>
            </div>
            <span
              v-if="isCurrentLive('ndi', src.name)"
              class="px-2 py-0.5 rounded-full text-[10px] font-bold bg-red-500 text-white uppercase tracking-wider shrink-0"
            >
              LIVE
            </span>
          </div>

          <div class="flex items-center justify-between text-xs text-slate-500">
            <span class="text-[11px] text-emerald-600 dark:text-emerald-400 font-semibold">NDI Center Crop</span>
            <span class="font-semibold text-indigo-600 dark:text-indigo-400 group-hover:underline">TAKE &rarr;</span>
          </div>
        </div>

        <!-- No NDI found fallback tile -->
        <div
          v-if="ndiSources.length === 0"
          class="p-4 rounded-xl border border-dashed border-slate-300 dark:border-slate-800 flex flex-col items-center justify-center text-center h-32 text-slate-400"
        >
          <Tv class="w-6 h-6 mb-1 opacity-50" />
          <span class="text-xs font-medium">No NDI Senders Found</span>
          <button
            @click="emit('refreshNdi')"
            class="text-[11px] text-indigo-600 dark:text-indigo-400 mt-1 font-semibold hover:underline"
          >
            Scan Network
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
