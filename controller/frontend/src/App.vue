<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import {
  Play,
  Square,
  Tv,
  Layers,
  Settings,
  Activity,
  Sun,
  Moon,
  Save,
  CheckCircle2,
  AlertTriangle,
  Radio,
  MonitorPlay,
  Maximize2,
  RefreshCw,
  Eye,
  EyeOff
} from 'lucide-vue-next'

// Theme Control
const isDark = ref(true)
function toggleColorMode() {
  isDark.value = !isDark.value
  if (isDark.value) {
    document.documentElement.classList.add('dark')
    localStorage.setItem('theme', 'dark')
  } else {
    document.documentElement.classList.remove('dark')
    localStorage.setItem('theme', 'light')
  }
}

// Active Tab
const activeTab = ref<'monitor' | 'canvas' | 'settings'>('monitor')

// State
interface DisplayMetrics {
  id: string
  name: string
  target: string
  is_connected: boolean
  fps: number
  bitrate_kbps: number
  packets_sent: number
  bytes_sent: number
  frames_sent: number
  drops: number
  last_sent_at: string
}

interface DisplayConfig {
  id: string
  name: string
  ip: string
  port: number
  width: number
  height: number
  crop_x: number
  crop_y: number
  bezel_padding_right: number
}

interface AppConfig {
  server: {
    http_port: number
    default_stream_port: number
    ws_ping_interval_sec: number
  }
  canvas: {
    width: number
    height: number
    fps: number
  }
  displays: DisplayConfig[]
  media: {
    default_mode: string
    video_file: string
  }
}

interface PipelineStatus {
  is_running: boolean
  active_mode: string
  video_file: string
}

interface NDISourceItem {
  name: string
  stream_name: string | null
  host_name: string | null
}

const isConnected = ref(false)
const pipeline = ref<PipelineStatus>({
  is_running: false,
  active_mode: 'testpattern',
  video_file: ''
})
const metrics = ref<DisplayMetrics[]>([])
const config = ref<AppConfig | null>(null)
const editableDisplays = ref<DisplayConfig[]>([])

const selectedMode = ref<'testpattern' | 'video' | 'ndi'>('testpattern')
const videoFilePath = ref('')
const selectedNdiSource = ref('')
const ndiSources = ref<NDISourceItem[]>([])
const isNdiScanning = ref(false)

const isActionLoading = ref(false)
const saveStatusMessage = ref('')
const saveStatusType = ref<'success' | 'error' | ''>('')
const previewKey = ref(Date.now())
const showCropOverlay = ref(true)
let previewErrorTimeout: any = null

function reloadPreview() {
  previewKey.value = Date.now()
}

function handlePreviewError() {
  if (!pipeline.value.is_running) return
  if (previewErrorTimeout) clearTimeout(previewErrorTimeout)
  previewErrorTimeout = setTimeout(() => {
    if (pipeline.value.is_running) {
      previewKey.value = Date.now()
    }
  }, 1000)
}

let ws: WebSocket | null = null

// Computed Stats
const totalBitrateMbps = computed(() => {
  const sumKbps = metrics.value.reduce((acc, m) => acc + (m.bitrate_kbps || 0), 0)
  return (sumKbps / 1000).toFixed(2)
})

const avgFps = computed(() => {
  if (metrics.value.length === 0) return '0.0'
  const sum = metrics.value.reduce((acc, m) => acc + (m.fps || 0), 0)
  return (sum / metrics.value.length).toFixed(1)
})

const totalDrops = computed(() => {
  return metrics.value.reduce((acc, m) => acc + (m.drops || 0), 0)
})

const sortedMetrics = computed(() => {
  return [...metrics.value].sort((a, b) =>
    a.id.localeCompare(b.id, undefined, { numeric: true, sensitivity: 'base' })
  )
})

const canvasWidth = computed(() => {
  if (editableDisplays.value.length > 0) {
    let max = 0
    for (const d of editableDisplays.value) {
      const rightEdge = Number(d.crop_x || 0) + Number(d.width || 0)
      if (rightEdge > max) max = rightEdge
    }
    if (max > 0) return max
  }
  return config.value?.canvas?.width || 5792
})

const canvasHeight = computed(() => {
  if (editableDisplays.value.length > 0) {
    let max = 0
    for (const d of editableDisplays.value) {
      const bottomEdge = Number(d.crop_y || 0) + Number(d.height || 0)
      if (bottomEdge > max) max = bottomEdge
    }
    if (max > 0) return max
  }
  return config.value?.canvas?.height || 540
})

const canvasFps = computed(() => {
  return config.value?.canvas?.fps || 30
})

// Methods
async function fetchConfig() {
  try {
    const res = await fetch('/api/config')
    if (res.ok) {
      const data: AppConfig = await res.json()
      config.value = data
      editableDisplays.value = JSON.parse(JSON.stringify(data.displays))
      selectedMode.value = (data.media?.default_mode as any) || 'testpattern'
      videoFilePath.value = data.media?.video_file || ''
    }
  } catch (err) {
    console.error('Failed to fetch config:', err)
  }
}

async function fetchNdiSources() {
  isNdiScanning.value = true
  try {
    const res = await fetch('/api/ndi/sources')
    if (res.ok) {
      const data = await res.json()
      ndiSources.value = data.sources || []
      if (!selectedNdiSource.value && ndiSources.value.length > 0) {
        selectedNdiSource.value = ndiSources.value[0].name
      }
    }
  } catch (err) {
    console.error('Failed to fetch NDI sources:', err)
  } finally {
    isNdiScanning.value = false
  }
}

async function handlePlay() {
  isActionLoading.value = true
  try {
    const res = await fetch('/api/playback', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        action: 'play',
        mode: selectedMode.value,
        video_file: videoFilePath.value,
        ndi_source: selectedNdiSource.value
      })
    })
    if (res.ok) {
      const data = await res.json()
      if (data.pipeline) pipeline.value = data.pipeline
    }
  } catch (err) {
    console.error('Play error:', err)
  } finally {
    isActionLoading.value = false
  }
}

async function handleStop() {
  isActionLoading.value = true
  try {
    const res = await fetch('/api/playback', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action: 'stop' })
    })
    if (res.ok) {
      const data = await res.json()
      if (data.pipeline) pipeline.value = data.pipeline
    }
  } catch (err) {
    console.error('Stop error:', err)
  } finally {
    isActionLoading.value = false
  }
}

async function handleSaveConfig() {
  saveStatusMessage.value = 'Saving configuration...'
  saveStatusType.value = ''
  try {
    const res = await fetch('/api/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ displays: editableDisplays.value })
    })
    if (res.ok) {
      saveStatusMessage.value = 'Configuration saved and applied successfully'
      saveStatusType.value = 'success'
      setTimeout(() => { saveStatusMessage.value = '' }, 3500)
      await fetchConfig()
    } else {
      saveStatusMessage.value = 'Failed to save configuration'
      saveStatusType.value = 'error'
    }
  } catch (err) {
    console.error('Save config error:', err)
    saveStatusMessage.value = 'Network error occurred'
    saveStatusType.value = 'error'
  }
}

function connectWebSocket() {
  if (ws) ws.close()

  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = `${proto}//${window.location.host}/ws`

  ws = new WebSocket(url)

  ws.onopen = () => {
    isConnected.value = true
  }

  ws.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data)
      if (data.pipeline) {
        const wasRunning = pipeline.value.is_running
        pipeline.value = data.pipeline
        if (wasRunning !== data.pipeline.is_running) {
          previewKey.value = Date.now()
        }
      }
      if (data.metrics && Array.isArray(data.metrics)) {
        metrics.value = data.metrics
      }
    } catch (e) {
      console.error('WS parse error:', e)
    }
  }

  ws.onclose = () => {
    isConnected.value = false
    setTimeout(connectWebSocket, 2000)
  }

  ws.onerror = () => {
    ws?.close()
  }
}

onMounted(() => {
  const savedTheme = localStorage.getItem('theme')
  if (savedTheme === 'light') {
    isDark.value = false
    document.documentElement.classList.remove('dark')
  } else {
    isDark.value = true
    document.documentElement.classList.add('dark')
  }

  fetchConfig()
  fetchNdiSources()
  connectWebSocket()
})

onUnmounted(() => {
  if (ws) ws.close()
})
</script>

<template>
  <div class="min-h-screen bg-slate-50 dark:bg-slate-950 text-slate-800 dark:text-slate-100 flex flex-col font-sans transition-colors duration-200">
    <!-- Top Navigation Bar -->
    <header class="border-b border-slate-200 dark:border-slate-800 bg-white/90 dark:bg-slate-900/80 backdrop-blur px-6 py-3 flex items-center justify-between sticky top-0 z-30 shadow-sm">
      <div class="flex items-center gap-5">
        <a href="/" class="flex items-center gap-4 py-0.5">
          <!-- Light theme logo -->
          <img src="/logo.svg" alt="TechnoTUT" class="h-10 w-auto max-w-[170px] object-contain dark:hidden" />
          <!-- Dark theme logo -->
          <img src="/logo_dark.svg" alt="TechnoTUT" class="h-10 w-auto max-w-[170px] object-contain hidden dark:block" />
          <div class="h-8 w-px bg-slate-200 dark:bg-slate-800"></div>
          <div>
            <h1 class="text-base font-bold tracking-tight text-slate-900 dark:text-slate-100">
              Booth Display Controller
            </h1>
            <p class="text-[11px] text-slate-500 dark:text-slate-400">The Utopia Tone Streaming Network</p>
          </div>
        </a>
      </div>

      <div class="flex items-center gap-3">
        <!-- Navigation Tabs -->
        <div class="flex bg-slate-100 dark:bg-slate-900 p-1 rounded-xl border border-slate-200 dark:border-slate-800">
          <button
            @click="activeTab = 'monitor'"
            :class="[
              'px-4 py-2 rounded-lg text-sm font-semibold transition flex items-center gap-2',
              activeTab === 'monitor'
                ? 'bg-[#C7000A] text-white shadow-md shadow-[#C7000A]/20'
                : 'text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-slate-100'
            ]"
          >
            <Activity class="w-4 h-4" />
            <span>Monitor & Control</span>
          </button>

          <button
            @click="activeTab = 'canvas'"
            :class="[
              'px-4 py-2 rounded-lg text-sm font-semibold transition flex items-center gap-2',
              activeTab === 'canvas'
                ? 'bg-[#C7000A] text-white shadow-md shadow-[#C7000A]/20'
                : 'text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-slate-100'
            ]"
          >
            <Layers class="w-4 h-4" />
            <span>Canvas Layout</span>
          </button>

          <button
            @click="activeTab = 'settings'"
            :class="[
              'px-4 py-2 rounded-lg text-sm font-semibold transition flex items-center gap-2',
              activeTab === 'settings'
                ? 'bg-[#C7000A] text-white shadow-md shadow-[#C7000A]/20'
                : 'text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-slate-100'
            ]"
          >
            <Settings class="w-4 h-4" />
            <span>Topology & Bezel</span>
          </button>
        </div>

        <!-- System Stats Badges -->
        <div class="hidden lg:flex items-center gap-2.5 bg-slate-100 dark:bg-slate-900 px-3.5 py-1.5 rounded-xl border border-slate-200 dark:border-slate-800 text-xs font-mono select-none">
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
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-xl border text-xs font-semibold"
          :class="isConnected
            ? 'border-emerald-200 dark:border-emerald-900 bg-emerald-50 dark:bg-emerald-950/40 text-emerald-600 dark:text-emerald-400'
            : 'border-rose-200 dark:border-rose-900 bg-rose-50 dark:bg-rose-950/40 text-rose-600 dark:text-rose-400'"
        >
          <span class="h-2 w-2 rounded-full" :class="isConnected ? 'bg-emerald-500 animate-pulse' : 'bg-rose-500'"></span>
          <span>{{ isConnected ? 'Online' : 'Offline' }}</span>
        </div>

        <!-- Dark/Light Theme Toggle -->
        <button
          @click="toggleColorMode"
          class="p-2 rounded-xl border border-slate-200 dark:border-slate-800 text-slate-500 hover:text-slate-900 dark:text-slate-400 dark:hover:text-slate-100 hover:bg-slate-100 dark:hover:bg-slate-800 transition"
          title="Toggle Theme"
        >
          <Sun v-if="isDark" class="w-5 h-5 text-amber-400" />
          <Moon v-else class="w-5 h-5 text-slate-600" />
        </button>
      </div>
    </header>

    <!-- Main Content Area -->
    <main class="flex-1 p-6 max-w-7xl mx-auto w-full space-y-6">

      <!-- ==================== TAB 1: MONITOR & CONTROL ==================== -->
      <div v-if="activeTab === 'monitor'" class="space-y-6">
        
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
                @click="handlePlay"
                :disabled="pipeline.is_running || isActionLoading"
                class="bg-[#C7000A] hover:bg-[#b00009] disabled:opacity-50 text-white font-semibold py-2.5 px-6 rounded-xl shadow-md shadow-[#C7000A]/20 transition flex items-center gap-2 text-sm cursor-pointer"
              >
                <Play class="w-4 h-4 fill-current" />
                <span>Start Stream</span>
              </button>

              <button
                @click="handleStop"
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
                v-model="selectedMode"
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
                v-model="videoFilePath"
                :disabled="pipeline.is_running"
                placeholder="/path/to/multi_screen_video.mp4"
                class="flex-1 bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-xl px-3.5 py-2 text-sm font-mono text-slate-800 dark:text-slate-100 focus:outline-none focus:ring-2 focus:ring-[#C7000A]"
              />
            </div>

            <!-- NDI Source Selector -->
            <div v-if="selectedMode === 'ndi'" class="flex-1 flex items-center gap-2 min-w-[280px]">
              <select
                v-model="selectedNdiSource"
                :disabled="pipeline.is_running"
                class="flex-1 bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-xl px-3.5 py-2 text-sm font-mono text-slate-800 dark:text-slate-100 focus:outline-none focus:ring-2 focus:ring-[#C7000A]"
              >
                <option value="" disabled>-- Select an NDI Source --</option>
                <option v-for="src in ndiSources" :key="src.name" :value="src.name">
                  {{ src.name }}
                </option>
              </select>

              <button
                @click="fetchNdiSources"
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
              Connected Panels ({{ metrics.length }})
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

      <!-- ==================== TAB 2: CANVAS & LAYOUT ==================== -->
      <div v-if="activeTab === 'canvas'" class="space-y-6">
        
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
                @click="showCropOverlay = !showCropOverlay"
                class="px-3 py-1.5 rounded-xl border text-xs font-medium flex items-center gap-1.5 transition cursor-pointer"
                :class="showCropOverlay ? 'bg-[#C7000A]/10 border-[#C7000A]/30 text-[#C7000A]' : 'bg-slate-100 dark:bg-slate-800 border-slate-200 dark:border-slate-700 text-slate-600 dark:text-slate-300'"
                :title="showCropOverlay ? 'Hide Panel Crop Overlay' : 'Show Panel Crop Overlay'"
              >
                <Eye v-if="showCropOverlay" class="w-3.5 h-3.5" />
                <EyeOff v-else class="w-3.5 h-3.5" />
                <span>Crop Overlay</span>
              </button>

              <button
                @click="reloadPreview"
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
              @error="handlePreviewError"
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
                @click="handlePlay"
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

      <!-- ==================== TAB 3: SETTINGS ==================== -->
      <div v-if="activeTab === 'settings'" class="space-y-6">
        
        <div class="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-6 shadow-sm space-y-6">
          <div class="flex items-center justify-between">
            <div>
              <h2 class="text-xs font-bold text-slate-500 dark:text-slate-400 uppercase tracking-wider mb-1">
                Display Topology & Bezel Configuration
              </h2>
              <p class="text-sm text-slate-600 dark:text-slate-300">
                Configure target IP address, UDP port, canvas crop offsets, and bezel padding for each panel.
              </p>
            </div>

            <button
              @click="handleSaveConfig"
              class="bg-[#C7000A] hover:bg-[#b00009] text-white font-semibold py-2 px-5 rounded-xl shadow-md shadow-[#C7000A]/20 transition flex items-center gap-2 text-sm cursor-pointer"
            >
              <Save class="w-4 h-4" />
              <span>Save & Apply Configuration</span>
            </button>
          </div>

          <!-- Alert message -->
          <div
            v-if="saveStatusMessage"
            class="p-3.5 rounded-xl text-sm font-medium flex items-center gap-2.5 transition"
            :class="saveStatusType === 'success' ? 'bg-emerald-50 dark:bg-emerald-950/50 text-emerald-700 dark:text-emerald-300 border border-emerald-200 dark:border-emerald-800' : 'bg-rose-50 dark:bg-rose-950/50 text-rose-700 dark:text-rose-300 border border-rose-200 dark:border-rose-800'"
          >
            <CheckCircle2 v-if="saveStatusType === 'success'" class="w-4 h-4 text-emerald-500" />
            <AlertTriangle v-else class="w-4 h-4 text-rose-500" />
            <span>{{ saveStatusMessage }}</span>
          </div>

          <!-- Displays Table -->
          <div class="overflow-x-auto border border-slate-200 dark:border-slate-800 rounded-xl">
            <table class="w-full text-left text-xs border-collapse">
              <thead>
                <tr class="bg-slate-50 dark:bg-slate-950 border-b border-slate-200 dark:border-slate-800 text-slate-600 dark:text-slate-400 font-semibold uppercase tracking-wider">
                  <th class="py-3 px-4">ID</th>
                  <th class="py-3 px-4">Display Name</th>
                  <th class="py-3 px-4">Target IP</th>
                  <th class="py-3 px-4">UDP Port</th>
                  <th class="py-3 px-4">Resolution (W × H)</th>
                  <th class="py-3 px-4">Crop Offset (X, Y)</th>
                  <th class="py-3 px-4">Right Bezel (px)</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-slate-100 dark:divide-slate-800">
                <tr
                  v-for="d in editableDisplays"
                  :key="d.id"
                  class="hover:bg-slate-50/50 dark:hover:bg-slate-800/40 transition"
                >
                  <td class="py-3 px-4 font-mono font-semibold text-slate-700 dark:text-slate-300">
                    {{ d.id }}
                  </td>
                  <td class="py-3 px-4">
                    <input
                      type="text"
                      v-model="d.name"
                      class="w-36 bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-lg px-2.5 py-1.5 text-xs text-slate-800 dark:text-slate-100 focus:outline-none focus:ring-1 focus:ring-[#C7000A]"
                    />
                  </td>
                  <td class="py-3 px-4">
                    <input
                      type="text"
                      v-model="d.ip"
                      class="w-32 font-mono bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-lg px-2.5 py-1.5 text-xs text-slate-800 dark:text-slate-100 focus:outline-none focus:ring-1 focus:ring-[#C7000A]"
                    />
                  </td>
                  <td class="py-3 px-4">
                    <input
                      type="number"
                      v-model.number="d.port"
                      class="w-20 font-mono bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-lg px-2.5 py-1.5 text-xs text-slate-800 dark:text-slate-100 focus:outline-none focus:ring-1 focus:ring-[#C7000A]"
                    />
                  </td>
                  <td class="py-3 px-4 font-mono">
                    <div class="flex items-center gap-1">
                      <input
                        type="number"
                        v-model.number="d.width"
                        class="w-16 bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-lg px-2 py-1 text-xs"
                      />
                      <span>×</span>
                      <input
                        type="number"
                        v-model.number="d.height"
                        class="w-16 bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-lg px-2 py-1 text-xs"
                      />
                    </div>
                  </td>
                  <td class="py-3 px-4 font-mono">
                    <div class="flex items-center gap-1">
                      <span class="text-slate-400">X:</span>
                      <input
                        type="number"
                        v-model.number="d.crop_x"
                        class="w-16 bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-lg px-2 py-1 text-xs"
                      />
                      <span class="text-slate-400 ml-1">Y:</span>
                      <input
                        type="number"
                        v-model.number="d.crop_y"
                        class="w-14 bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-lg px-2 py-1 text-xs"
                      />
                    </div>
                  </td>
                  <td class="py-3 px-4 font-mono">
                    <input
                      type="number"
                      v-model.number="d.bezel_padding_right"
                      class="w-20 bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 rounded-lg px-2.5 py-1.5 text-xs text-slate-800 dark:text-slate-100 focus:outline-none focus:ring-1 focus:ring-[#C7000A]"
                    />
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>

    </main>

    <!-- Footer -->
    <footer class="border-t border-slate-200 dark:border-slate-800 py-4 px-6 text-center text-xs text-slate-500 dark:text-slate-400">
      <span>BOOTH-DISPLAY controller - TechnoTUT</span>
    </footer>
  </div>
</template>
