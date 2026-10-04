<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import AppHeader from './components/AppHeader.vue'
import MonitorTab from './components/MonitorTab.vue'
import CanvasTab from './components/CanvasTab.vue'
import SettingsTab from './components/SettingsTab.vue'
import type {
  DisplayMetrics,
  DisplayConfig,
  AppConfig,
  PipelineStatus,
  NDISourceItem
} from './types'

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
    <AppHeader
      :active-tab="activeTab"
      :pipeline="pipeline"
      :is-connected="isConnected"
      :total-bitrate-mbps="totalBitrateMbps"
      :avg-fps="avgFps"
      :is-dark="isDark"
      @update:active-tab="activeTab = $event"
      @toggle-theme="toggleColorMode"
    />

    <!-- Main Content Area -->
    <main class="flex-1 p-6 max-w-7xl mx-auto w-full space-y-6">
      <!-- TAB 1: MONITOR & CONTROL -->
      <MonitorTab
        v-if="activeTab === 'monitor'"
        :pipeline="pipeline"
        :is-action-loading="isActionLoading"
        v-model:selected-mode="selectedMode"
        v-model:video-file-path="videoFilePath"
        v-model:selected-ndi-source="selectedNdiSource"
        :ndi-sources="ndiSources"
        :is-ndi-scanning="isNdiScanning"
        :sorted-metrics="sortedMetrics"
        @play="handlePlay"
        @stop="handleStop"
        @fetch-ndi-sources="fetchNdiSources"
      />

      <!-- TAB 2: CANVAS & LAYOUT -->
      <CanvasTab
        v-if="activeTab === 'canvas'"
        :pipeline="pipeline"
        :is-action-loading="isActionLoading"
        :preview-key="previewKey"
        :show-crop-overlay="showCropOverlay"
        :canvas-width="canvasWidth"
        :canvas-height="canvasHeight"
        :canvas-fps="canvasFps"
        :editable-displays="editableDisplays"
        @play="handlePlay"
        @reload-preview="reloadPreview"
        @preview-error="handlePreviewError"
        @toggle-crop-overlay="showCropOverlay = !showCropOverlay"
      />

      <!-- TAB 3: SETTINGS -->
      <SettingsTab
        v-if="activeTab === 'settings'"
        :editable-displays="editableDisplays"
        :save-status-message="saveStatusMessage"
        :save-status-type="saveStatusType"
        @save-config="handleSaveConfig"
      />
    </main>

    <!-- Footer -->
    <footer class="border-t border-slate-200 dark:border-slate-800 py-4 px-6 text-center text-xs text-slate-500 dark:text-slate-400">
      <span>BOOTH-DISPLAY controller - TechnoTUT</span>
    </footer>
  </div>
</template>
