// ชั้นเรียก Go (Wails) + state ที่ใช้ร่วมกันทั้งแอป
import * as Go from '../wailsjs/go/main/App'
import { EventsOn } from '../wailsjs/runtime/runtime'

const inWails = () => typeof window !== 'undefined' && !!window['go']

export const api = new Proxy({}, {
  get: (_, name) => async (...args) => {
    if (!inWails()) throw new Error('ต้องเปิดผ่านแอป Wails (wails dev / wails build) จึงจะเรียก Go ได้')
    return await Go[name](...args)
  }
})

export function errMsg(e) {
  return (e && (e.message || e.toString())) || 'เกิดข้อผิดพลาด'
}

export function useAppState() {
  return useState('app-state', () => ({
    source: '', dest: '', sourceCount: 0,
    aiEnabled: false, hasAiKey: false, aiModel: '', dbPath: '',
    restored: false, integrityNote: ''
  }))
}

export function useToasts() {
  const toasts = useState('toasts', () => [])
  let id = 0
  const push = (text, kind = 'info', ms = 4500) => {
    const t = { id: ++id, text, kind }
    toasts.value = [...toasts.value, t]
    setTimeout(() => { toasts.value = toasts.value.filter(x => x.id !== t.id) }, ms)
  }
  return { toasts, push, error: (e) => push(errMsg(e), 'error', 7000), ok: (t) => push(t, 'ok') }
}

export async function refreshState() {
  const st = useAppState()
  st.value = await api.GetState()
  return st.value
}

// progress ระหว่าง Backup/วิเคราะห์ (Go ส่ง event ชื่อ "progress")
export function useProgress() {
  return useState('progress', () => ({ active: false, phase: '', index: 0, total: 0, name: '', ai: 0 }))
}

let listening = false
export function listenProgress() {
  if (listening || !inWails()) return
  listening = true
  const p = useProgress()
  EventsOn('progress', (ev) => {
    p.value = { active: true, phase: ev.phase, index: ev.index, total: ev.total, name: ev.name, ai: ev.ai || 0 }
  })
}

// เพิ่มตัวนับเพื่อให้แกลเลอรีโหลดใหม่หลังมีการเปลี่ยนแปลงข้อมูล
export function useDataVersion() {
  return useState('data-version', () => 0)
}

export function fmtSize(n) {
  if (n == null) return ''
  if (n < 1024) return n + ' B'
  if (n < 1048576) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1048576).toFixed(1) + ' MB'
}
