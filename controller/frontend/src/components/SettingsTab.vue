<script setup lang="ts">
import {
  Save,
  CheckCircle2,
  AlertTriangle
} from 'lucide-vue-next'
import type { DisplayConfig } from '../types'

defineProps<{
  editableDisplays: DisplayConfig[]
  saveStatusMessage: string
  saveStatusType: 'success' | 'error' | ''
}>()

const emit = defineEmits<{
  (e: 'saveConfig'): void
}>()
</script>

<template>
  <div class="space-y-6">
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
          @click="emit('saveConfig')"
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
</template>
