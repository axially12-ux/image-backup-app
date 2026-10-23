# Image Backup (Wails + Nuxt)

โปรแกรม Backup รูปภาพจาก Flash Drive / โทรศัพท์ — Final Project

| ข้อกำหนด | ทำอย่างไร |
|---|---|
| Wails + Frontend เป็น Nuxt | `frontend/` คือ Nuxt 3 (SPA, `ssr:false`) → `nuxt generate` ออกที่ `frontend/dist` ให้ Wails ฝัง |
| SQLite + GORM | `internal/gormstore` (GORM + `glebarez/sqlite` pure Go ไม่ต้องใช้ CGO) |
| Backup เทียบเท่ากลางภาค | `internal/core/service.go` — ดูตารางด้านล่าง |
| วิเคราะห์ภาพ (คำอธิบาย/Tag) | `internal/core/analyze*.go` — ในเครื่อง (EXIF+สี+แสง) และ Vision AI (ตัวเลือก) |
| ค้นหาด้วย Tag + Tag Cloud | แท็บ "รูปภาพ / ค้นหา" |
| ค้นหาด้วยคำอธิบาย + ช่องกรอก | ช่องค้นหาในแท็บเดียวกัน (หลายคำ = ต้องมีครบทุกคำ, ใช้ร่วมกับ Tag ได้) |
| Windows + macOS | pure-Go SQLite, path ผ่าน `filepath`, dialog เลือกโฟลเดอร์ของ Wails |

## เทียบกับงานกลางภาค (flashbackup CLI)

| คำสั่งกลางภาค | ในแอป | โค้ด |
|---|---|---|
| `/source <path>` | ช่อง Source + ปุ่มเลือกโฟลเดอร์ แสดงจำนวนรูปที่พบ | `SetSource` |
| `/dest <path>` | ช่อง Dest (สร้างโฟลเดอร์ให้ถ้ายังไม่มี) + **Integrity scan ทันที** | `SetDest` |
| `/list source` `/list dest` | แท็บ Backup / "ไฟล์ต้นทาง/ปลายทาง" | `ListSource` `ListDest` |
| `/list db [path]` | แท็บ "ประวัติใน DB" (ว่าง = dest ปัจจุบัน, ยังไม่ตั้ง dest → แจ้งเตือนไม่ crash) | `ListDB` |
| `/move <f1,f2>` / `/move all` | "Backup ที่เลือก" / "Backup ทั้งหมด" | `Move` |
| ↳ ข้ามไฟล์ซ้ำ (ดิสก์ + DB) | ชื่อซ้ำถูกข้าม (+ เนื้อหาซ้ำด้วย SHA-256) | `moveOne` |
| ↳ ข้ามไดรฟ์ = Copy+Delete | `os.Rename` → ไม่ได้ก็ copy + ตรวจขนาด + ลบต้นทาง | `moveFile` |
| ↳ จับเวลา (ms / s) | แสดง "เวลารวม" หลัง Backup | `FormatElapsed` |
| `/check` | แท็บ "ตรวจสอบ (Check)" รายงานไฟล์สูญหาย | `Check` |
| `/delete [source\|dest] ...` / `all` | ลบที่เลือก / ทั้งหมด (dest ลบประวัติ DB ด้วย) | `Delete` |
| จำ session ล่าสุด (ตาราง settings) | เปิดแอปใหม่กู้ Source/Dest + สแกน integrity ให้ | `New`, `RestoredNote` |

ส่วนที่เพิ่มจากกลางภาค: โหมดคัดลอก (copy) นอกเหนือจากย้าย, รองรับนามสกุลตัวพิมพ์ใหญ่ (`.JPG`), รายงานไฟล์ที่ไม่อยู่ใน DB, progress bar, ไม่ส่งรูปเป็น base64 (เสิร์ฟรูปย่อผ่าน `/media/thumb/<id>`)

## วิเคราะห์ภาพ

* **ในเครื่อง (ค่าเริ่มต้น, ออฟไลน์)** — อ่าน EXIF (กล้อง, วัน-เวลา → ช่วงเช้า/เย็น…, GPS), แนวภาพ/ความละเอียด, สีเด่น, ความสว่าง, ขาวดำ, ท้องฟ้า/ธรรมชาติ → คำอธิบายภาษาไทย + Tag
  *ข้อจำกัด:* ตรวจ "สิ่งของในภาพ" (คน/แมว/อาหาร) ไม่ได้
* **Vision AI (ตัวเลือก)** — ⚙ ตั้งค่า → ใส่ Google Gemini API Key (ขอฟรีที่ aistudio.google.com/apikey) → ส่งภาพ (ย่อ ≤1568px) ให้ Gemini อธิบาย + สร้าง Tag; ล้มเหลวจะ fallback เป็นแบบในเครื่อง
  แล้วกด "วิเคราะห์ทุกรูปใหม่" เพื่ออัปเดตรูปเดิม
  * **Tag เป็นหมวดหมู่** — AI เลือกได้เฉพาะรายการ `AICategories` (ทะเล ภูเขา ธรรมชาติ ผู้หญิง ผู้ชาย ฯลฯ, ไม่เกิน 5 หมวด/รูป, ~55 หมวดแบ่งเป็นกลุ่ม สถานที่/คน/กิจกรรม/สัตว์/อาหาร/ยานพาหนะ) บังคับทั้งใน prompt, `responseSchema` (enum) และตรวจซ้ำฝั่ง Go — แก้รายการที่ `internal/core/analyze_ai.go`
  * **รองรับ 1,000+ รูป และเร็วขึ้น** — โควตา free tier นับเป็น *request ต่อวัน* ไม่ใช่จำนวนรูป จึงส่ง 16 รูป/request (1,000 รูป ≈ 63 request), ย่อรูปเหลือ 768px, เตรียมรูปแบบขนานตามจำนวน core, ปิด thinking ของโมเดล, ยิง 8 request พร้อมกัน (ลดอัตโนมัติเมื่อโดน 429; ถ้าโมเดลติด 503/429 ต่อเนื่องจะสลับไปโมเดล flash ตัวอื่นของ key เดียวกันอัตโนมัติ), งบเวลา AI 6 นาที (`Service.AIBudget`) เกินแล้วที่เหลือใช้แบบในเครื่อง (`Service.AIBatchSize`, `AIConcurrency`); 429 รายนาทีรอแล้วลองใหม่ (เคารพ `Retry-After`), โควตารายวันหมดจะหยุดยิงทันทีแล้วใช้แบบในเครื่องแทน; ชุดที่ตอบเพี้ยนจะแบ่งครึ่งลองใหม่

## วิธีรัน / build

ต้องมี Go ≥ 1.25, Node ≥ 20, [Wails v2](https://wails.io) (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`)

```bash
go mod tidy                 # ครั้งแรก (ดึง dependency ให้ครบ)
cd frontend && npm install && cd ..
wails dev                   # โหมดพัฒนา (Nuxt dev server พอร์ต 3000)
wails build                 # ได้ build/bin/image-backup-app(.exe)
```

* Windows: `wails build` ได้ `.exe` — macOS: รัน `wails build` **บนเครื่อง Mac** (หรือ `-platform darwin/universal`)
* ข้อมูลเก็บที่ `~/ImageBackupData/` (`imagebackup.db` + แคชรูปย่อ)

## ทดสอบ

```bash
go test ./internal/core/...
```
ครอบคลุม move/copy, ข้ามไฟล์ซ้ำ, cross-device fallback, check/delete, state guard, restore session, ค้นหา/Tag Cloud, วิเคราะห์ภาพ, EXIF, Vision AI (เซิร์ฟเวอร์จำลอง), media handler
