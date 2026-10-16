<template>
  <div class="stack">
    <div class="card stack">
      <h3>1) เลือกต้นทางงงงงงงงงงงงงงงงงง (Flash Drive / โทรศัพท์) และปลายทางงงงงงงงงงงงงงงงงงงงงงงงงงงงงงงงงงงงงงงง</h3>
      <div class="row">
        <span style="width:70px" class="muted">Source</span>
        <input class="grow" type="text" v-model="srcInput" placeholder="เช่น E:\DCIM หรือ /Volumes/USB/DCIM" @keyup.enter="applySource" />
        <button class="btn" @click="pickSource">เลือกโฟลเดอร์…</button>
        <button class="btn primary" @click="applySource">ตั้งค่า</button>
      </div>
      <div class="row">
        <span style="width:70px" class="muted">Dest</span>
        <input class="grow" type="text" v-model="dstInput" placeholder="โฟลเดอร์ที่จะเก็บรูป Backup" @keyup.enter="applyDest" />
        <button class="btn" @click="pickDest">เลือกโฟลเดอร์…</button>
        <button class="btn primary" @click="applyDest">ตั้งค่า</button>
      </div>
      <div class="muted" v-if="st.source">พบรูปใน Source {{ st.sourceCount }} ไฟล์ · โปรแกรมจำค่านี้ไว้ใช้ครั้งถัดไปโดยอัตโนมัติ</div>
      <div v-if="destCheck" :class="destCheck.ok ? 'chip ok' : 'chip warn'">
        Integrity scan: {{ destCheck.ok ? `ตรวจ ${destCheck.verified} ไฟล์ ครบถ้วน` : `พบไฟล์สูญหาย ${destCheck.missing.length} รายการ: ${destCheck.missing.join(', ')}` }}
      </div>
    </div>

    <div class="card stack">
      <div class="row">
        <h3 class="grow" style="margin:0">2) เลือกรูปที่จะ Backup</h3>
        <label class="inline"><input type="radio" value="move" v-model="mode" /> ย้าย (move — ลบจากต้นทาง)</label>
        <label class="inline"><input type="radio" value="copy" v-model="mode" /> คัดลอก (copy)</label>
      </div>
      <div class="row">
        <button class="btn small" @click="loadSource" :disabled="!st.source">โหลดรายการใหม่</button>
        <label class="inline"><input type="checkbox" :checked="allSelected" @change="toggleAll" /> เลือกทั้งหมด ({{ selected.size }}/{{ files.length }})</label>
        <div class="grow"></div>
        <button class="btn primary" :disabled="busy || !selected.size || !st.dest" @click="run(false)">Backup ที่เลือก ({{ selected.size }})</button>
        <button class="btn primary" :disabled="busy || !files.length || !st.dest" @click="run(true)">Backup ทั้งหมด</button>
      </div>
      <div class="filelist" v-if="files.length">
        <table>
          <tbody>
            <tr v-for="f in files" :key="f.name">
              <td style="width:30px"><input type="checkbox" :checked="selected.has(f.name)" @change="toggle(f.name)" /></td>
              <td>{{ f.name }}</td>
              <td class="muted mono">{{ fmtSize(f.size) }}</td>
              <td class="muted mono">{{ f.modTime }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="muted">{{ st.source ? 'ไม่พบไฟล์รูปในโฟลเดอร์ต้นทาง' : 'ยังไม่ได้ตั้ง Source' }}</div>

      <div v-if="busy" class="stack">
        <div class="muted">
          {{ prog.phase === 'analyze' ? 'กำลังวิเคราะห์ภาพ' : 'กำลังย้ายไฟล์' }}
          {{ prog.index }}/{{ prog.total }} {{ prog.name }}
        </div>
        <div class="progress"><div :style="{ width: pct + '%' }"></div></div>
      </div>
    </div>

    <div class="card stack" v-if="report">
      <h3>ผลการ Backup</h3>
      <div class="row" style="flex-wrap:wrap">
        <span class="chip ok">สำเร็จ {{ report.moved }}</span>
        <span class="chip warn">ข้าม {{ report.skipped }}</span>
        <span class="chip err">ผิดพลาด {{ report.failed }}</span>
        <span class="chip">วิเคราะห์ {{ report.analyzed }} รูป<template v-if="report.aiUsed"> (AI {{ report.aiUsed }})</template></span>
        <span class="chip">⏱ เวลารวม {{ report.elapsed }}</span>
        <span class="chip muted">ย้าย {{ report.moveMs.toFixed(1) }} ms · วิเคราะห์ {{ report.analyzeMs.toFixed(1) }} ms</span>
      </div>
      <div v-if="report.note" class="chip warn" style="white-space:normal">{{ report.note }}</div>
      <div class="filelist">
        <table>
          <thead><tr><th>ไฟล์</th><th>สถานะ</th><th>รายละเอียด</th></tr></thead>
          <tbody>
            <tr v-for="it in report.items" :key="it.name">
              <td>{{ it.name }}</td>
              <td :class="'st-' + it.status">{{ statusLabel[it.status] || it.status }}</td>
              <td class="muted">{{ it.message }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<script setup>
const st = useAppState()
const prog = useProgress()
const version = useDataVersion()
const { error, ok } = useToasts()

const srcInput = ref('')
const dstInput = ref('')
const files = ref([])
const selected = ref(new Set())
const mode = ref('move')
const busy = ref(false)
const report = ref(null)
const destCheck = ref(null)
const statusLabel = { moved: 'ย้ายแล้ว', copied: 'คัดลอกแล้ว', skipped: 'ข้าม (ซ้ำ)', error: 'ผิดพลาด', deleted: 'ลบแล้ว' }

const allSelected = computed(() => files.value.length > 0 && selected.value.size === files.value.length)
const pct = computed(() => (prog.value.total ? Math.round((prog.value.index / prog.value.total) * 100) : 0))

watch(() => [st.value.source, st.value.dest], ([s, d]) => {
  if (s && !srcInput.value) srcInput.value = s
  if (d && !dstInput.value) dstInput.value = d
}, { immediate: true })

watch(() => st.value.source, (s) => { if (s) loadSource() }, { immediate: true })

async function loadSource() {
  try {
    files.value = await api.ListSource()
    selected.value = new Set()
  } catch (e) {
    files.value = []
  }
}

const toggle = (n) => {
  const s = new Set(selected.value)
  s.has(n) ? s.delete(n) : s.add(n)
  selected.value = s
}
const toggleAll = () => {
  selected.value = allSelected.value ? new Set() : new Set(files.value.map(f => f.name))
}

async function applySource() {
  try {
    const n = await api.SetSource(srcInput.value)
    await refreshState()
    ok(`ตั้ง Source แล้ว พบรูป ${n} ไฟล์`)
  } catch (e) { error(e) }
}
async function applyDest() {
  try {
    destCheck.value = await api.SetDest(dstInput.value)
    await refreshState()
    version.value++
    ok('ตั้ง Destination แล้ว')
  } catch (e) { error(e) }
}
async function pickSource() {
  try {
    const p = await api.PickSource()
    if (p) { srcInput.value = p; await applySource() }
  } catch (e) { error(e) }
}
async function pickDest() {
  try {
    const p = await api.PickDest()
    if (p) { dstInput.value = p; await applyDest() }
  } catch (e) { error(e) }
}

async function run(all) {
  busy.value = true
  report.value = null
  prog.value = { active: true, phase: 'move', index: 0, total: 0, name: '' }
  try {
    report.value = await api.Move(all ? [] : [...selected.value], all, mode.value)
    await refreshState()
    await loadSource()
    version.value++
  } catch (e) {
    error(e)
  } finally {
    busy.value = false
    prog.value = { ...prog.value, active: false }
  }
}
</script>
