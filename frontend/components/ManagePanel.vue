<template>
  <div class="stack">
    <div class="row">
      <button v-for="v in views" :key="v.id" class="btn" :class="{ primary: view === v.id }" @click="view = v.id">{{ v.label }}</button>
      <div class="grow"></div>
      <button class="btn small" @click="load">🔄 โหลดใหม่</button>
    </div>

    <!-- ไฟล์จริงใน Destination -->
    <div class="card stack" v-if="view === 'dest'">
      <h3>ไฟล์ใน Destination (ในดิสก์จริง)</h3>
      <div class="muted mono">{{ st.dest || 'ยังไม่ได้ตั้ง Destination' }}</div>
      <FileTable :files="destFiles" v-model:selected="selDest" />
      <div class="row">
        <button class="btn danger" :disabled="!selDest.size" @click="del('dest', false)">ลบที่เลือก ({{ selDest.size }})</button>
        <button class="btn danger" :disabled="!destFiles.length" @click="del('dest', true)">ลบทั้งหมด</button>
      </div>
    </div>

    <!-- ไฟล์ใน Source -->
    <div class="card stack" v-if="view === 'source'">
      <h3>ไฟล์ใน Source (ในดิสก์จริง)</h3>
      <div class="muted mono">{{ st.source || 'ยังไม่ได้ตั้ง Source' }}</div>
      <FileTable :files="srcFiles" v-model:selected="selSrc" />
      <div class="row">
        <button class="btn danger" :disabled="!selSrc.size" @click="del('source', false)">ลบที่เลือก ({{ selSrc.size }})</button>
        <button class="btn danger" :disabled="!srcFiles.length" @click="del('source', true)">ลบทั้งหมด</button>
      </div>
    </div>

    <!-- ประวัติใน DB -->
    <div class="card stack" v-if="view === 'db'">
      <h3>ประวัติการ Backup ใน Database</h3>
      <div class="row">
        <input class="grow" type="text" v-model="dbPath" placeholder="ว่าง = ของ Destination ปัจจุบัน หรือใส่ path ปลายทางอื่นเพื่อดูประวัติของโฟลเดอร์นั้น" @keyup.enter="load" />
        <button class="btn" @click="load">แสดง</button>
      </div>
      <div class="filelist">
        <table>
          <thead><tr><th>#</th><th>ไฟล์</th><th>วิเคราะห์โดย</th><th>Tags</th></tr></thead>
          <tbody>
            <tr v-for="r in dbRows" :key="r.id">
              <td class="mono muted">{{ r.id }}</td>
              <td>{{ r.filename }}</td>
              <td class="muted">{{ r.analyzer || '-' }}</td>
              <td class="muted">{{ (r.tags || []).map(t => t.name).join(', ') }}</td>
            </tr>
            <tr v-if="!dbRows.length"><td colspan="4" class="muted">(ไม่พบประวัติ)</td></tr>
          </tbody>
        </table>
      </div>
      <div class="muted mono" v-if="st.dbPath">ไฟล์ฐานข้อมูล: {{ st.dbPath }}</div>
    </div>

    <!-- Integrity check -->
    <div class="card stack" v-if="view === 'check'">
      <h3>ตรวจสอบความสมบูรณ์ (Database เทียบกับไฟล์จริง)</h3>
      <div><button class="btn primary" @click="runCheck">▶ ตรวจสอบตอนนี้</button></div>
      <template v-if="check">
        <div class="row" style="flex-wrap:wrap">
          <span class="chip">ในฐานข้อมูล {{ check.records }}</span>
          <span class="chip ok">ตรวจผ่าน {{ check.verified }}</span>
          <span class="chip" :class="check.missing.length ? 'err' : 'ok'">สูญหาย {{ check.missing.length }}</span>
          <span class="chip" :class="check.untracked.length ? 'warn' : ''">ไม่มีในฐานข้อมูล {{ check.untracked.length }}</span>
        </div>
        <div v-if="check.missing.length">
          <div class="st-error">⚠ ไฟล์ที่อยู่ใน Database แต่หายจากดิสก์:</div>
          <ul><li v-for="m in check.missing" :key="m">{{ m }}</li></ul>
          <button class="btn danger small" @click="cleanMissing">ลบประวัติของไฟล์ที่หายออกจาก DB</button>
        </div>
        <div v-if="check.untracked.length">
          <div class="st-skipped">มีไฟล์รูปในโฟลเดอร์ปลายทางที่ไม่ได้ Backup ผ่านโปรแกรม:</div>
          <ul><li v-for="m in check.untracked" :key="m">{{ m }}</li></ul>
        </div>
        <div v-if="check.ok" class="st-moved">✔ ไฟล์ทั้งหมดที่บันทึกไว้ยังอยู่ครบ</div>
      </template>
    </div>

    <div class="card" v-if="lastDelete">
      <h3>ผลการลบ: {{ lastDelete.deleted }} รายการ</h3>
      <table><tbody>
        <tr v-for="it in lastDelete.items" :key="it.name">
          <td>{{ it.name }}</td><td :class="'st-' + it.status">{{ it.message }}</td>
        </tr>
      </tbody></table>
    </div>
  </div>
</template>

<script setup>
const props = defineProps({ active: Boolean })
const st = useAppState()
const version = useDataVersion()
const { error } = useToasts()

const views = [
  { id: 'dest', label: 'ไฟล์ปลายทาง' }, { id: 'source', label: 'ไฟล์ต้นทาง' },
  { id: 'db', label: 'ประวัติใน DB' }, { id: 'check', label: 'ตรวจสอบ (Check)' }
]
const view = ref('dest')
const destFiles = ref([]); const srcFiles = ref([]); const dbRows = ref([])
const selDest = ref(new Set()); const selSrc = ref(new Set())
const dbPath = ref('')
const check = ref(null)
const lastDelete = ref(null)

async function load() {
  try {
    if (view.value === 'dest') destFiles.value = st.value.dest ? await api.ListDest() : []
    if (view.value === 'source') srcFiles.value = st.value.source ? await api.ListSource() : []
    if (view.value === 'db') dbRows.value = (await api.ListDB(dbPath.value)) || []
  } catch (e) { error(e) }
}

async function runCheck() {
  try { check.value = await api.Check() } catch (e) { error(e) }
}

async function del(scope, all) {
  const sel = scope === 'dest' ? selDest.value : selSrc.value
  const what = all ? `ทั้งหมดใน ${scope === 'dest' ? 'Destination' : 'Source'}` : `${sel.size} ไฟล์ที่เลือก`
  if (!confirm(`ยืนยันลบ ${what}? การลบไม่สามารถกู้คืนได้`)) return
  try {
    lastDelete.value = await api.Delete(scope, all ? [] : [...sel], all)
    selDest.value = new Set(); selSrc.value = new Set()
    await refreshState()
    version.value++
    await load()
  } catch (e) { error(e) }
}

async function cleanMissing() {
  try {
    lastDelete.value = await api.Delete('dest', check.value.missing, false)
    version.value++
    await runCheck()
  } catch (e) { error(e) }
}

watch(view, load)
watch(() => props.active, (a) => { if (a) load() })
watch(() => version.value, () => { if (props.active) load() })
</script>
