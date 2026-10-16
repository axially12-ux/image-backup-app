// ให้ frontend/dist มีไฟล์อย่างน้อย 1 ไฟล์เสมอ ไม่งั้น go:embed จะ error
import { mkdirSync, readdirSync, writeFileSync } from 'node:fs'

mkdirSync('dist', { recursive: true })
if (readdirSync('dist').length === 0) {
  writeFileSync('dist/index.html', '<!doctype html><title>placeholder</title>')
}
