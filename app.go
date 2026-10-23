package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"image-backup-app/internal/core"
	"image-backup-app/internal/gormstore"
)

// App คือชั้นบางๆ ที่เปิดความสามารถของ core.Service ให้ Frontend (Nuxt) เรียกผ่าน Wails
// ตรรกะทั้งหมด (เทียบเท่าคำสั่ง /source /dest /list /move /check /delete ของงานกลางภาค)
// อยู่ใน internal/core ส่วนฐานข้อมูล GORM + SQLite อยู่ใน internal/gormstore
type App struct {
	ctx     context.Context
	svc     *core.Service
	initErr error
	note    string // ผลสแกน integrity ตอนเปิดโปรแกรม
	restore bool
}

func NewApp() *App {
	a := &App{}
	a.init()
	return a
}

// dataDir คือโฟลเดอร์เก็บฐานข้อมูล/แคชรูปย่อ (ข้ามแพลตฟอร์ม: ~/ImageBackupData)
func dataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "./app_data"
	}
	return filepath.Join(home, "ImageBackupData")
}

func (a *App) init() {
	dir := dataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.initErr = err
		return
	}
	dbPath := filepath.Join(dir, "imagebackup.db")
	store, err := gormstore.Open(dbPath)
	if err != nil {
		a.initErr = err
		return
	}
	svc, err := core.New(store)
	if err != nil {
		a.initErr = err
		return
	}
	svc.DBPath = dbPath
	svc.ThumbDir = filepath.Join(dir, "thumbs")
	svc.OnProgress = func(ev core.ProgressEvent) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "progress", ev)
		}
	}
	a.svc = svc
	st := svc.GetState()
	a.restore = st.Source != "" || st.Dest != ""
	a.note = svc.RestoredNote()
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

func (a *App) ready() error {
	if a.initErr != nil {
		return a.initErr
	}
	if a.svc == nil {
		return errors.New("ระบบยังไม่พร้อม")
	}
	return nil
}

// ---------- state ----------

func (a *App) GetState() (core.State, error) {
	if err := a.ready(); err != nil {
		return core.State{}, err
	}
	st := a.svc.GetState()
	st.Restored = a.restore
	st.IntegrityNote = a.note
	return st, nil
}

func (a *App) SaveAISettings(in core.AISettingsInput) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.svc.SaveAISettings(in)
}

// ---------- /source /dest ----------

func (a *App) PickSource() (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	p, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "เลือกโฟลเดอร์ต้นทาง / Flash Drive / โทรศัพท์"})
	if err != nil || p == "" {
		return "", err
	}
	return p, nil
}

func (a *App) PickDest() (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	p, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:                "เลือกโฟลเดอร์ปลายทางสำหรับเก็บรูป Backup",
		CanCreateDirectories: true,
	})
	if err != nil || p == "" {
		return "", err
	}
	return p, nil
}

func (a *App) SetSource(path string) (int, error) {
	if err := a.ready(); err != nil {
		return 0, err
	}
	return a.svc.SetSource(path)
}

func (a *App) SetDest(path string) (core.CheckReport, error) {
	if err := a.ready(); err != nil {
		return core.CheckReport{}, err
	}
	return a.svc.SetDest(path)
}

// ---------- /list ----------

func (a *App) ListSource() ([]core.FileEntry, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.svc.ListSource()
}

func (a *App) ListDest() ([]core.FileEntry, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.svc.ListDest()
}

func (a *App) ListDB(path string) ([]core.Image, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.svc.ListDB(path)
}

// ---------- /move /check /delete ----------

func (a *App) Move(names []string, all bool, mode string) (core.MoveReport, error) {
	if err := a.ready(); err != nil {
		return core.MoveReport{}, err
	}
	return a.svc.Move(names, all, mode)
}

func (a *App) Check() (core.CheckReport, error) {
	if err := a.ready(); err != nil {
		return core.CheckReport{}, err
	}
	return a.svc.Check()
}

func (a *App) Delete(scope string, names []string, all bool) (core.DeleteReport, error) {
	if err := a.ready(); err != nil {
		return core.DeleteReport{}, err
	}
	return a.svc.Delete(scope, names, all)
}

// ---------- ค้นหา / Tag Cloud / วิเคราะห์ ----------

func (a *App) Search(keyword, tag string) ([]core.Image, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.svc.Search(keyword, tag)
}

func (a *App) TagCloud() ([]core.TagCount, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.svc.TagCloud()
}

// ReanalyzePending วิเคราะห์เฉพาะรูปที่ AI ยังไม่ได้ทำ (เช่น รอบก่อนติด 503/หมดเวลา)
func (a *App) ReanalyzePending() (core.AnalyzeReport, error) {
	if err := a.ready(); err != nil {
		return core.AnalyzeReport{}, err
	}
	return a.svc.ReanalyzePending()
}

func (a *App) Reanalyze(ids []uint, all bool) (core.AnalyzeReport, error) {
	if err := a.ready(); err != nil {
		return core.AnalyzeReport{}, err
	}
	return a.svc.Reanalyze(ids, all)
}
