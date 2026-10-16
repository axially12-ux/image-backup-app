<template>
  <div class="stack">
    <TagCloud :tags="cloud" :selected="tag" @pick="pickTag" />

    <div class="card stack">
      <div class="row">
        <input class="grow" type="search" v-model="keyword" placeholder="🔍 ค้นหาจากคำอธิบายภาพ (พิมพ์หลายคำได้ เช่น  แนวนอน ท้องฟ้า)" />
        <button class="btn" v-if="keyword || tag" @click="clearAll">ล้างตัวกรอง</button>
      </div>
      <div class="row" v-if="tag">
        กำลังกรองด้วย Tag: <span class="chip ok">{{ tag }}</span>
        <button class="btn small" @click="tag = ''">ยกเลิก Tag</button>
      </div>
      <div class="muted">แสดง {{ images.length }} รูป<template v-if="!st.dest"> — ยังไม่ได้ตั้ง Destination</template></div>
    </div>

    <div class="grid" v-if="images.length">
      <button v-for="im in images" :key="im.id" class="thumb" @click="open = im">
        <img :src="'/media/thumb/' + im.id" :alt="im.filename" loading="lazy" />
        <div class="meta">
          <div>{{ im.filename }}</div>
          <div class="desc">{{ im.description }}</div>
        </div>
      </button>
    </div>
    <div v-else class="card muted" style="text-align:center">ไม่พบรูปภาพ</div>

    <div class="modal-bg" v-if="open" @click.self="open = null">
      <div class="modal">
        <div class="row" style="margin-bottom:10px">
          <h3 class="grow" style="margin:0">{{ open.filename }}</h3>
          <button class="btn small" @click="open = null">ปิด ✕</button>
        </div>
        <div class="detail">
          <img :src="'/media/full/' + open.id" :alt="open.filename" />
          <div class="stack">
            <div>
              <div class="muted">คำอธิบาย <span class="chip">{{ open.analyzer === 'ai' ? 'Vision AI' : 'วิเคราะห์ในเครื่อง' }}</span></div>
              <p style="margin:6px 0">{{ open.description || '— ยังไม่ได้วิเคราะห์ —' }}</p>
            </div>
            <div>
              <div class="muted" style="margin-bottom:6px">Tags (คลิกเพื่อค้นหา)</div>
              <div class="row" style="flex-wrap:wrap">
                <button v-for="t in open.tags" :key="t.id" class="chip" style="cursor:pointer;color:var(--accent)" @click="pickTag(t.name); open = null">#{{ t.name }}</button>
              </div>
            </div>
            <div class="muted mono">{{ open.width }}×{{ open.height }} · {{ fmtSize(open.size) }}</div>
            <div class="row">
              <button class="btn small" :disabled="busy" @click="reanalyze(open)">วิเคราะห์ภาพนี้ใหม่</button>
              <button class="btn small danger" :disabled="busy" @click="remove(open)">ลบรูปนี้</button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
const props = defineProps({ active: Boolean })
const st = useAppState()
const version = useDataVersion()
const { error, ok } = useToasts()

const keyword = ref('')
const tag = ref('')
const images = ref([])
const cloud = ref([])
const open = ref(null)
const busy = ref(false)
let timer = null

async function load() {
  if (!st.value.dest) { images.value = []; cloud.value = []; return }
  try {
    const [im, cl] = await Promise.all([api.Search(keyword.value, tag.value), api.TagCloud()])
    images.value = im || []
    cloud.value = cl || []
  } catch (e) { error(e) }
}

// ค้นหาแบบ debounce กันยิงทุกตัวอักษร
watch([keyword, tag], () => { clearTimeout(timer); timer = setTimeout(load, 220) })
watch(() => [version.value, st.value.dest], load)
watch(() => props.active, (a) => { if (a) load() })
onMounted(load)

const pickTag = (n) => { tag.value = tag.value === n ? '' : n }
const clearAll = () => { keyword.value = ''; tag.value = '' }

async function reanalyze(im) {
  busy.value = true
  try {
    const r = await api.Reanalyze([im.id], false)
    ok(`วิเคราะห์ใหม่แล้ว (${r.elapsed})`)
    if (r.note) error(r.note)
    await load()
    open.value = images.value.find(x => x.id === im.id) || null
  } catch (e) { error(e) } finally { busy.value = false }
}

async function remove(im) {
  if (!confirm(`ลบ ${im.filename} ออกจากโฟลเดอร์ปลายทางและฐานข้อมูล?`)) return
  busy.value = true
  try {
    await api.Delete('dest', [im.filename], false)
    open.value = null
    version.value++
  } catch (e) { error(e) } finally { busy.value = false }
}
</script>
