// Package core คือตรรกะหลักของโปรแกรม Backup รูปภาพ (ไม่ผูกกับ Wails/GORM)
//
// แยกออกมาเพื่อให้ทดสอบได้ง่าย: Service ทำงานผ่าน interface Store
// (ตัวจริงคือ internal/gormstore ที่ใช้ GORM + SQLite)
package core

import "time"

// ===================== โมเดลฐานข้อมูล (GORM) =====================

// Image คือประวัติการ Backup รูป 1 รูป ผูกกับโฟลเดอร์ปลายทาง (Dest)
// เหมือน FileRecord ในงานกลางภาค (dest + filename) แต่เพิ่มผลวิเคราะห์ภาพ
type Image struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Dest        string    `gorm:"not null;uniqueIndex:idx_dest_file" json:"dest"`
	Filename    string    `gorm:"not null;uniqueIndex:idx_dest_file" json:"filename"`
	Hash        string    `gorm:"index" json:"hash"`
	Size        int64     `json:"size"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	Description string    `json:"description"`
	Analyzer    string    `json:"analyzer"` // "local" | "ai" | "" (ยังไม่วิเคราะห์)
	Tags        []Tag     `gorm:"many2many:image_tags;" json:"tags"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Tag คือป้ายกำกับ (ชื่อไม่ซ้ำ) ใช้ร่วมกันหลายรูปแบบ many-to-many
type Tag struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"not null;uniqueIndex" json:"name"`
}

// Settings เก็บค่าล่าสุดของ source/dest (เหมือนตาราง settings งานกลางภาค)
// และการตั้งค่าตัววิเคราะห์ภาพ มีแถวเดียวเสมอ (upsert)
type Settings struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Source    string `json:"source"`
	Dest      string `json:"dest"`
	AIEnabled bool   `json:"aiEnabled"`
	AIKey     string `json:"-"`
	AIModel   string `json:"aiModel"`
}

// TagCount ใช้วาด Tag Cloud
type TagCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ===================== ผลลัพธ์ที่ส่งให้ Frontend =====================

// FileEntry คือไฟล์ที่มีอยู่จริงในโฟลเดอร์
type FileEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// State คือสถานะปัจจุบันของโปรแกรม
type State struct {
	Source        string `json:"source"`
	Dest          string `json:"dest"`
	SourceCount   int    `json:"sourceCount"`
	AIEnabled     bool   `json:"aiEnabled"`
	HasAIKey      bool   `json:"hasAiKey"`
	AIModel       string `json:"aiModel"`
	DBPath        string `json:"dbPath"`
	Restored      bool   `json:"restored"`
	IntegrityNote string `json:"integrityNote"`
}

// AISettingsInput คือค่าที่ผู้ใช้กรอกในหน้าตั้งค่า
type AISettingsInput struct {
	Enabled  bool   `json:"enabled"`
	Key      string `json:"key"` // ว่าง = ไม่เปลี่ยน
	Model    string `json:"model"`
	ClearKey bool   `json:"clearKey"`
}

// ItemResult คือผลของไฟล์ 1 ไฟล์ในการ move/delete
type ItemResult struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // moved | copied | skipped | error | deleted
	Message string `json:"message"`
	Method  string `json:"method"`
}

// MoveReport คือสรุปผลการ Backup ทั้งชุด
type MoveReport struct {
	Items     []ItemResult `json:"items"`
	Moved     int          `json:"moved"`
	Skipped   int          `json:"skipped"`
	Failed    int          `json:"failed"`
	MoveMs    float64      `json:"moveMs"`
	AnalyzeMs float64      `json:"analyzeMs"`
	TotalMs   float64      `json:"totalMs"`
	Elapsed   string       `json:"elapsed"` // เช่น "532.10 ms" หรือ "2.41 s"
	Mode      string       `json:"mode"`
	Analyzed  int          `json:"analyzed"`
	AIUsed    int          `json:"aiUsed"`
	Note      string       `json:"note"`
}

// DeleteReport คือสรุปผลการลบ
type DeleteReport struct {
	Items   []ItemResult `json:"items"`
	Deleted int          `json:"deleted"`
	Scope   string       `json:"scope"`
}

// CheckReport คือผล Integrity Check (DB เทียบกับไฟล์จริง)
type CheckReport struct {
	Dest      string   `json:"dest"`
	Records   int      `json:"records"`
	Verified  int      `json:"verified"`
	Missing   []string `json:"missing"`   // อยู่ใน DB แต่หายจากดิสก์
	Untracked []string `json:"untracked"` // เป็นรูปในโฟลเดอร์ แต่ไม่มีใน DB
	OK        bool     `json:"ok"`
}

// ProgressEvent ส่งให้ UI ระหว่างทำงานนาน ๆ
type ProgressEvent struct {
	Phase  string `json:"phase"` // move | analyze
	Index  int    `json:"index"`
	Total  int    `json:"total"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Store คือชั้นฐานข้อมูล (GORM + SQLite ในงานจริง)
type Store interface {
	LoadSettings() (Settings, bool, error) // bool = เคยบันทึกมาก่อนหรือไม่
	SaveSettings(Settings) error

	FindImage(dest, filename string) (*Image, error) // ไม่พบ -> nil, nil
	FindByHash(dest, hash string) (*Image, error)
	GetImage(id uint) (*Image, error)
	CreateImage(img *Image, tags []string) error
	UpdateAnalysis(id uint, description, analyzer string, width, height int, tags []string) error
	DeleteImage(dest, filename string) error
	ListImages(dest string) ([]Image, error)
	Search(dest string, keywords []string, tag string) ([]Image, error)
	TagCounts(dest string) ([]TagCount, error)
}
