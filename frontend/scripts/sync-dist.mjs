// คัดลอกผลลัพธ์ของ nuxt generate เข้า frontend/dist
import { cpSync, rmSync } from 'node:fs'

rmSync('dist', { recursive: true, force: true })
cpSync('.output/public', 'dist', { recursive: true })
