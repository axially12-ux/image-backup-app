<template>
  <div class="shell">
    <header class="topbar">
      <h1>📷 Image Backup</h1>
      <span class="chip" v-if="st.dest" :title="st.dest">ปลายทาง: {{ shortPath(st.dest) }}</span>
      <span class="chip" :class="st.aiEnabled ? 'ok' : ''">วิเคราะห์ภาพ: {{ st.aiEnabled ? 'Vision AI' : 'ในเครื่อง' }}</span>
      <div class="grow"></div>
      <button class="btn small" @click="showSettings = true">⚙ ตั้งค่า</button>
    </header>

    <nav class="tabs">
      <button v-for="t in tabs" :key="t.id" class="tab" :class="{ active: tab === t.id }" @click="tab = t.id">{{ t.label }}</button>
    </nav>

    <main class="content">
      <div v-if="st.integrityNote && !noteDismissed" class="card" style="margin-bottom:14px">
        <div class="row">
          <span class="grow">ℹ {{ st.integrityNote }}</span>
          <button class="btn small" @click="noteDismissed = true">ปิด</button>
        </div>
      </div>
      <BackupPanel v-show="tab === 'backup'" />
      <GalleryPanel v-show="tab === 'gallery'" :active="tab === 'gallery'" />
      <ManagePanel v-show="tab === 'manage'" :active="tab === 'manage'" />
    </main>

    <SettingsModal v-if="showSettings" @close="showSettings = false" />

    <div class="toasts">
      <div v-for="t in toasts" :key="t.id" class="toast" :class="t.kind">{{ t.text }}</div>
    </div>
  </div>
</template>

<script setup>
const st = useAppState()
const { toasts, error } = useToasts()
const tab = ref('backup')
const showSettings = ref(false)
const noteDismissed = ref(false)
const tabs = [
  { id: 'backup', label: '📂 Backup' },
  { id: 'gallery', label: '🖼 รูปภาพ / ค้นหา' },
  { id: 'manage', label: '🗂 จัดการ / ตรวจสอบ' }
]

const shortPath = (p) => (p.length > 42 ? '…' + p.slice(-41) : p)

onMounted(async () => {
  listenProgress()
  try {
    await refreshState()
    if (st.value.dest) tab.value = 'gallery'
  } catch (e) {
    error(e)
  }
})
</script>
