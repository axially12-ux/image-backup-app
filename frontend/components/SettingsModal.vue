<template>
  <div class="modal-bg" @click.self="$emit('close')">
    <div class="modal" style="max-width:620px">
      <div class="row" style="margin-bottom:12px">
        <h3 class="grow" style="margin:0">⚙ ตั้งค่าการวิเคราะห์ภาพ</h3>
        <button class="btn small" @click="$emit('close')">ปิด ✕</button>
      </div>
      <div class="stack">
        <div class="card">
          <b>วิเคราะห์ในเครื่อง (ค่าเริ่มต้น — ออฟไลน์)</b>
          <p class="muted" style="margin:6px 0 0">
            อ่าน EXIF (กล้อง/วันเวลาถ่าย/GPS) ขนาดและแนวภาพ วิเคราะห์สีเด่น ความสว่าง ภาพขาวดำ ท้องฟ้า/ธรรมชาติ
            แล้วสร้างคำอธิบายภาษาไทยและ Tag อัตโนมัติ
          </p>
        </div>
        <div class="card stack">
          <label class="inline"><input type="checkbox" v-model="enabled" /> <b>ใช้ Vision AI (Google Gemini)</b> เพื่อรู้ว่า "ในภาพมีอะไร"</label>
          <p class="muted" style="margin:0">รูปจะถูกย่อแล้วส่งไปที่ Google (generativelanguage.googleapis.com) ต้องต่ออินเทอร์เน็ตและมี API Key — ขอฟรีได้ที่ aistudio.google.com/apikey (free tier Google อาจนำข้อมูลไปปรับปรุงผลิตภัณฑ์ และมีเพดานคำขอต่อนาที/วัน ถ้า AI ล้มเหลวหรือติดเพดานจะใช้การวิเคราะห์ในเครื่องแทนอัตโนมัติ) — ระบบส่งหลายรูปต่อคำขอ (16 รูป/ครั้ง) เพื่อประหยัดโควตา และจัด Tag เป็นหมวดหมู่ เช่น ทะเล ธรรมชาติ ผู้หญิง ผู้ชาย</p>
          <div>
            <div class="muted">API Key <span v-if="st.hasAiKey" class="chip ok">บันทึกไว้แล้ว</span></div>
            <input type="password" v-model="key" :placeholder="st.hasAiKey ? '(เว้นว่างเพื่อใช้ key เดิม)' : 'AIza...'" autocomplete="off" />
          </div>
          <div>
            <div class="muted">Model</div>
            <input type="text" v-model="model" />
          </div>
          <div class="muted">Key เก็บในฐานข้อมูลบนเครื่องนี้เท่านั้น (ไม่เข้ารหัส) — อย่าใช้บนเครื่องสาธารณะ</div>
          <div class="row">
            <button class="btn primary" @click="save">บันทึก</button>
            <button class="btn danger" v-if="st.hasAiKey" @click="clearKey">ลบ Key</button>
          </div>
        </div>
        <div class="card stack">
          <b>วิเคราะห์รูปที่ Backup แล้วใหม่ทั้งหมด</b>
          <div class="muted">ใช้เมื่อเพิ่งเปิด Vision AI หรืออยากให้คำอธิบายใหม่ (ใช้ตัววิเคราะห์ตามที่ตั้งค่าไว้ด้านบน)</div>
          <div class="row">
            <button class="btn primary" :disabled="busy || !st.dest" @click="reanalyzePending">{{ busy ? 'กำลังวิเคราะห์…' : 'วิเคราะห์เฉพาะรูปที่ AI ยังไม่ได้ทำ' }}</button>
            <button class="btn" :disabled="busy || !st.dest" @click="reanalyzeAll">วิเคราะห์ทุกรูปใหม่</button>
          </div>
          <div v-if="busy" class="stack">
            <div class="progress"><div :style="{ width: pct + '%' }"></div></div>
            <div v-if="prog.total">
              ทำไปแล้ว <b>{{ prog.index }}/{{ prog.total }}</b> รูป ({{ pct }}%) · ได้จาก AI <b>{{ prog.ai }}</b> รูป · ใช้เวลา {{ elapsed }} วิ
            </div>
            <div v-else class="muted">กำลังส่งรูปให้ AI… (ผลชุดแรกอาจใช้เวลาสักครู่ ถ้า Gemini ติดคิว) · {{ elapsed }} วิ</div>
          </div>
          <div class="muted">ปุ่มแรกเหมาะหลังรอบที่ AI ติด 503/หมดเวลา: ทำต่อเฉพาะรูปที่ยังไม่ได้หมวดหมู่จาก AI ไม่เสียโควตากับรูปที่ทำแล้ว</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
const emit = defineEmits(['close'])
const st = useAppState()
const version = useDataVersion()
const { error, ok } = useToasts()
const enabled = ref(st.value.aiEnabled)
const key = ref('')
const model = ref(st.value.aiModel || 'gemini-3.1-flash-lite')
const busy = ref(false)
const prog = useProgress()
const pct = computed(() => (prog.value.total ? Math.round((prog.value.index / prog.value.total) * 100) : 0))
// นับเวลาที่ผ่านไประหว่างวิเคราะห์ (แสดงให้เห็นว่าไม่ได้ค้าง)
const elapsed = ref(0)
let timer = null
watch(busy, (b) => {
  clearInterval(timer)
  if (b) {
    elapsed.value = 0
    prog.value = { active: true, phase: 'analyze', index: 0, total: 0, name: '', ai: 0 }
    const t0 = Date.now()
    timer = setInterval(() => { elapsed.value = Math.round((Date.now() - t0) / 1000) }, 500)
  }
})
onBeforeUnmount(() => clearInterval(timer))

async function save() {
  try {
    await api.SaveAISettings({ enabled: enabled.value, key: key.value, model: model.value, clearKey: false })
    await refreshState()
    ok('บันทึกการตั้งค่าแล้ว')
    emit('close')
  } catch (e) { error(e); await refreshState(); enabled.value = st.value.aiEnabled }
}
async function clearKey() {
  try {
    await api.SaveAISettings({ enabled: false, key: '', model: model.value, clearKey: true })
    enabled.value = false
    await refreshState()
    ok('ลบ API Key แล้ว')
  } catch (e) { error(e) }
}
async function reanalyzePending() {
  busy.value = true
  try {
    const r = await api.ReanalyzePending()
    ok(`วิเคราะห์ ${r.done}/${r.total} รูป (AI ${r.aiUsed}) (${r.elapsed})`)
    if (r.detail) ok('⏱ ' + r.detail)
    if (r.note) error(r.note)
    version.value++
  } catch (e) { error(e) } finally { busy.value = false }
}
async function reanalyzeAll() {
  busy.value = true
  try {
    const r = await api.Reanalyze([], true)
    ok(`วิเคราะห์ ${r.done}/${r.total} รูป (${r.elapsed})`)
    if (r.detail) ok('⏱ ' + r.detail)
    if (r.note) error(r.note)
    version.value++
  } catch (e) { error(e) } finally { busy.value = false }
}
</script>
