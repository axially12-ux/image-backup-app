package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNoSource / ErrNoDest ทำหน้าที่เป็น "State Guard" เหมือนงานกลางภาค
// คือสั่งคำสั่งก่อนตั้งค่า จะได้ข้อความแจ้งเตือน ไม่ crash
var (
	ErrNoSource = errors.New("ยังไม่ได้ตั้ง Source (โฟลเดอร์ต้นทาง/Flash Drive)")
	ErrNoDest   = errors.New("ยังไม่ได้ตั้ง Destination (โฟลเดอร์ปลายทาง)")
)

// Service รวมความสามารถ Backup ทั้งหมด (เทียบเท่าคำสั่ง /source /dest /list
// /move /check /delete ของงานกลางภาค)
type Service struct {
	store Store

	mu       sync.RWMutex // ป้องกัน state
	source   string
	dest     string
	settings Settings

	opMu   sync.Mutex // ให้ย้าย/ลบ/วิเคราะห์ ทำทีละงาน
	emitMu sync.Mutex

	// OnProgress ถูกเรียกระหว่าง move/analyze (ตั้งค่าได้, อาจเป็น nil)
	OnProgress func(ProgressEvent)

	DBPath         string
	ThumbDir       string
	AIBaseURL      string // ใช้ทดสอบ; ค่าว่าง = https://api.anthropic.com
	AnalyzeWorkers int
}

// New สร้าง Service และกู้ค่า source/dest ล่าสุดจาก DB (ถ้ามี)
func New(store Store) (*Service, error) {
	s := &Service{store: store, AnalyzeWorkers: 3}
	st, found, err := store.LoadSettings()
	if err != nil {
		return nil, err
	}
	if found {
		s.settings = st
		s.source = st.Source
		s.dest = st.Dest
	}
	if s.settings.AIModel == "" {
		s.settings.AIModel = DefaultAIModel
	}
	return s, nil
}

// emit ส่ง progress ทีละ event (serialize เพราะ worker วิเคราะห์ภาพทำงานขนาน)
func (s *Service) emit(ev ProgressEvent) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	if s.OnProgress != nil {
		s.OnProgress(ev)
	}
}

func (s *Service) paths() (string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.source, s.dest
}

// ===================== state / settings =====================

// GetState คืนสถานะปัจจุบัน
func (s *Service) GetState() State {
	s.mu.RLock()
	st := State{
		Source:    s.source,
		Dest:      s.dest,
		AIEnabled: s.settings.AIEnabled,
		HasAIKey:  s.settings.AIKey != "",
		AIModel:   s.settings.AIModel,
		DBPath:    s.DBPath,
	}
	s.mu.RUnlock()
	if st.Source != "" {
		if n, err := countImages(st.Source); err == nil {
			st.SourceCount = n
		}
	}
	return st
}

// RestoredNote สแกน integrity ของ dest ที่กู้คืนมา (ใช้ตอนเปิดโปรแกรม)
func (s *Service) RestoredNote() string {
	_, dest := s.paths()
	if dest == "" {
		return "ยังไม่มี session ก่อนหน้า กรุณาเลือก Source และ Destination"
	}
	rep, err := s.Check()
	if err != nil {
		return err.Error()
	}
	if rep.OK {
		return fmt.Sprintf("กู้ค่า session ล่าสุดแล้ว — ตรวจสอบ %d ไฟล์ครบถ้วน", rep.Verified)
	}
	return fmt.Sprintf("กู้ค่า session ล่าสุดแล้ว — พบไฟล์สูญหาย %d รายการ", len(rep.Missing))
}

func (s *Service) saveSettingsLocked() error {
	s.settings.Source = s.source
	s.settings.Dest = s.dest
	return s.store.SaveSettings(s.settings)
}

// SaveAISettings บันทึกการตั้งค่าตัววิเคราะห์ภาพ
func (s *Service) SaveAISettings(in AISettingsInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.AIEnabled = in.Enabled
	if in.ClearKey {
		s.settings.AIKey = ""
	} else if strings.TrimSpace(in.Key) != "" {
		s.settings.AIKey = strings.TrimSpace(in.Key)
	}
	if strings.TrimSpace(in.Model) != "" {
		s.settings.AIModel = strings.TrimSpace(in.Model)
	}
	if s.settings.AIEnabled && s.settings.AIKey == "" {
		s.settings.AIEnabled = false
		_ = s.saveSettingsLocked()
		return errors.New("ต้องใส่ API Key ก่อนจึงเปิดใช้ Vision AI ได้")
	}
	return s.saveSettingsLocked()
}

// ===================== /source =====================

// SetSource ตั้งโฟลเดอร์ต้นทาง และคืนจำนวนรูปที่พบ
func (s *Service) SetSource(path string) (int, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, errors.New("กรุณาระบุ path ของ Source")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return 0, fmt.Errorf("ไม่พบโฟลเดอร์ Source หรือไม่ใช่โฟลเดอร์: %s", path)
	}
	clean := filepath.Clean(path)
	n, err := countImages(clean)
	if err != nil {
		return 0, fmt.Errorf("อ่านโฟลเดอร์ Source ไม่ได้: %w", err)
	}
	s.mu.Lock()
	s.source = clean
	err = s.saveSettingsLocked()
	s.mu.Unlock()
	return n, err
}

// ===================== /dest =====================

// SetDest ตั้งโฟลเดอร์ปลายทาง (สร้างให้ถ้ายังไม่มี) แล้วสแกน integrity ทันที
func (s *Service) SetDest(path string) (CheckReport, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return CheckReport{}, errors.New("กรุณาระบุ path ของ Destination")
	}
	info, err := os.Stat(path)
	if err != nil {
		if mkErr := os.MkdirAll(path, 0o755); mkErr != nil {
			return CheckReport{}, fmt.Errorf("สร้างโฟลเดอร์ Destination ไม่ได้: %w", mkErr)
		}
	} else if !info.IsDir() {
		return CheckReport{}, fmt.Errorf("Destination มีอยู่แล้วแต่ไม่ใช่โฟลเดอร์: %s", path)
	}
	s.mu.Lock()
	s.dest = filepath.Clean(path)
	err = s.saveSettingsLocked()
	s.mu.Unlock()
	if err != nil {
		return CheckReport{}, err
	}
	return s.Check()
}

// ===================== /list =====================

// ListSource คืนรายการรูปที่อยู่ใน Source จริง
func (s *Service) ListSource() ([]FileEntry, error) {
	src, _ := s.paths()
	if src == "" {
		return nil, ErrNoSource
	}
	return listImageFiles(src)
}

// ListDest คืนรายการรูปที่อยู่ใน Destination จริง
func (s *Service) ListDest() ([]FileEntry, error) {
	_, dest := s.paths()
	if dest == "" {
		return nil, ErrNoDest
	}
	return listImageFiles(dest)
}

// ListDB คืนประวัติ Backup ใน DB ของ path ที่ระบุ (ว่าง = Destination ปัจจุบัน)
// ถ้าไม่ได้ระบุและยังไม่ตั้ง Destination จะแจ้ง error (State Guard)
func (s *Service) ListDB(path string) ([]Image, error) {
	scope := strings.TrimSpace(path)
	if scope == "" {
		_, scope = s.paths()
	} else {
		scope = filepath.Clean(scope)
	}
	if scope == "" {
		return nil, errors.New("ยังไม่ได้ตั้ง Destination — ตั้ง Destination หรือระบุ path ของประวัติที่ต้องการ")
	}
	return s.store.ListImages(scope)
}

// ===================== /check =====================

// Check เทียบ DB (ของ Destination ปัจจุบัน) กับไฟล์จริง รายงานไฟล์ที่สูญหาย
func (s *Service) Check() (CheckReport, error) {
	_, dest := s.paths()
	if dest == "" {
		return CheckReport{}, ErrNoDest
	}
	recs, err := s.store.ListImages(dest)
	if err != nil {
		return CheckReport{}, err
	}
	rep := CheckReport{Dest: dest, Records: len(recs), Missing: []string{}, Untracked: []string{}}
	known := map[string]bool{}
	for _, r := range recs {
		known[r.Filename] = true
		if fileExists(filepath.Join(dest, r.Filename)) {
			rep.Verified++
		} else {
			rep.Missing = append(rep.Missing, r.Filename)
		}
	}
	if files, err := listImageFiles(dest); err == nil {
		for _, f := range files {
			if !known[f.Name] {
				rep.Untracked = append(rep.Untracked, f.Name)
			}
		}
	}
	rep.OK = len(rep.Missing) == 0
	return rep, nil
}

// ===================== /move =====================

// Move ย้าย (mode="move") หรือคัดลอก (mode="copy") รูปจาก Source ไป Destination
// names ว่าง + all=true คือทั้งหมด ข้ามไฟล์ซ้ำ (เช็คทั้งดิสก์และ DB) บันทึกลง DB
// แล้ววิเคราะห์ภาพ (description/tag) พร้อมจับเวลาทั้งชุด
func (s *Service) Move(names []string, all bool, mode string) (MoveReport, error) {
	src, dest := s.paths()
	if src == "" {
		return MoveReport{}, ErrNoSource
	}
	if dest == "" {
		return MoveReport{}, ErrNoDest
	}
	if mode != "copy" {
		mode = "move"
	}
	if samePath(src, dest) {
		return MoveReport{}, errors.New("Source และ Destination เป็นโฟลเดอร์เดียวกัน")
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	targets, err := resolveTargets(src, names, all)
	if err != nil {
		return MoveReport{}, err
	}
	if len(targets) == 0 {
		return MoveReport{}, errors.New("ไม่มีรูปให้ Backup")
	}

	rep := MoveReport{Mode: mode, Items: []ItemResult{}}
	totalStart := time.Now()
	moveStart := time.Now()

	type pending struct {
		id   uint
		name string
	}
	var toAnalyze []pending

	for i, name := range targets {
		res := s.moveOne(src, dest, name, mode)
		rep.Items = append(rep.Items, res.ItemResult)
		switch res.Status {
		case "moved", "copied":
			rep.Moved++
			if res.imageID != 0 {
				toAnalyze = append(toAnalyze, pending{res.imageID, name})
			}
		case "skipped":
			rep.Skipped++
		default:
			rep.Failed++
		}
		s.emit(ProgressEvent{Phase: "move", Index: i + 1, Total: len(targets), Name: name, Status: res.Status})
	}
	rep.MoveMs = ms(time.Since(moveStart))

	// ---- วิเคราะห์ภาพ (คำอธิบาย + Tag) ----
	anaStart := time.Now()
	ids := make([]uint, 0, len(toAnalyze))
	for _, p := range toAnalyze {
		ids = append(ids, p.id)
	}
	rep.Analyzed, rep.AIUsed, rep.Note = s.analyzeIDs(ids)
	rep.AnalyzeMs = ms(time.Since(anaStart))

	rep.TotalMs = ms(time.Since(totalStart))
	rep.Elapsed = FormatElapsed(time.Since(totalStart))
	return rep, nil
}

type moveResult struct {
	ItemResult
	imageID uint
}

func (s *Service) moveOne(src, dest, name, mode string) moveResult {
	fail := func(status, msg string) moveResult {
		return moveResult{ItemResult: ItemResult{Name: name, Status: status, Message: msg}}
	}
	srcFile := filepath.Join(src, name)
	dstFile := filepath.Join(dest, name)

	if !fileExists(srcFile) {
		return fail("error", "ไม่พบไฟล์ใน Source")
	}
	if !IsImageName(name) {
		return fail("error", "ไม่ใช่ไฟล์รูปภาพที่รองรับ")
	}

	// ตรวจไฟล์ซ้ำ: ชื่อซ้ำในดิสก์ปลายทาง หรือ ใน DB
	existing, err := s.store.FindImage(dest, name)
	if err != nil {
		return fail("error", "อ่านฐานข้อมูลไม่ได้: "+err.Error())
	}
	if existing != nil || fileExists(dstFile) {
		return fail("skipped", "ไฟล์ซ้ำ: พบชื่อนี้ใน Database / Destination แล้ว")
	}
	// เนื้อหาซ้ำ (ชื่อต่างแต่เป็นรูปเดียวกัน)
	hash, size, err := hashFile(srcFile)
	if err != nil {
		return fail("error", "อ่านไฟล์ไม่ได้: "+err.Error())
	}
	if dup, err := s.store.FindByHash(dest, hash); err == nil && dup != nil && fileExists(filepath.Join(dest, dup.Filename)) {
		return fail("skipped", fmt.Sprintf("เนื้อหารูปซ้ำกับ %s ที่ Backup ไว้แล้ว", dup.Filename))
	}

	var method string
	status := "moved"
	if mode == "copy" {
		if err := copyFile(srcFile, dstFile); err != nil {
			return fail("error", "คัดลอกไม่สำเร็จ: "+err.Error())
		}
		method, status = "Copy", "copied"
	} else {
		method, err = moveFile(srcFile, dstFile)
		if err != nil {
			return fail("error", "ย้ายไม่สำเร็จ: "+err.Error())
		}
	}

	rec := &Image{Dest: dest, Filename: name, Hash: hash, Size: size}
	if err := s.store.CreateImage(rec, []string{"backup"}); err != nil {
		return fail("error", "ย้ายไฟล์แล้วแต่บันทึกลงฐานข้อมูลไม่สำเร็จ: "+err.Error())
	}
	return moveResult{
		ItemResult: ItemResult{Name: name, Status: status, Method: method, Message: "บันทึกลง DB ผ่าน GORM แล้ว (" + method + ")"},
		imageID:    rec.ID,
	}
}

// moveFile ลอง os.Rename ก่อน (เร็ว ใช้ได้เมื่ออยู่ไดรฟ์เดียวกัน) ถ้าไม่ได้
// (ข้ามไดรฟ์) จะ fallback เป็นคัดลอกทั้งไฟล์ ตรวจขนาด แล้วลบต้นทาง
func moveFile(src, dst string) (string, error) {
	if err := os.Rename(src, dst); err == nil {
		return "Direct Move", nil
	}
	if err := copyFile(src, dst); err != nil {
		os.Remove(dst)
		return "", err
	}
	si, e1 := os.Stat(src)
	di, e2 := os.Stat(dst)
	if e1 != nil || e2 != nil || si.Size() != di.Size() {
		os.Remove(dst)
		return "", errors.New("ขนาดไฟล์หลังคัดลอกไม่ตรงกับต้นฉบับ")
	}
	if err := os.Remove(src); err != nil {
		return "", fmt.Errorf("คัดลอกแล้วแต่ลบไฟล์ต้นทางไม่ได้: %w", err)
	}
	return "Cross-device Copy+Delete", nil
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if fi, e := os.Stat(src); e == nil {
		_ = os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	}
	return nil
}

// ===================== /delete =====================

// Delete ลบรูปใน scope "dest" (ค่าเริ่มต้น, ลบประวัติใน DB ด้วย) หรือ "source"
func (s *Service) Delete(scope string, names []string, all bool) (DeleteReport, error) {
	if scope != "source" {
		scope = "dest"
	}
	src, dest := s.paths()
	dir := dest
	if scope == "source" {
		dir = src
	}
	if dir == "" {
		if scope == "source" {
			return DeleteReport{}, ErrNoSource
		}
		return DeleteReport{}, ErrNoDest
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()

	var targets []string
	if all {
		files, err := listImageFiles(dir)
		if err != nil {
			return DeleteReport{}, err
		}
		for _, f := range files {
			targets = append(targets, f.Name)
		}
		if scope == "dest" { // รวมรายการที่เหลือแต่ใน DB (ไฟล์หายไปแล้ว)
			if recs, err := s.store.ListImages(dest); err == nil {
				seen := map[string]bool{}
				for _, t := range targets {
					seen[t] = true
				}
				for _, r := range recs {
					if !seen[r.Filename] {
						targets = append(targets, r.Filename)
					}
				}
			}
		}
	} else {
		for _, n := range names {
			if n = strings.TrimSpace(n); n != "" {
				targets = append(targets, n)
			}
		}
	}
	if len(targets) == 0 {
		return DeleteReport{}, errors.New("ไม่มีไฟล์ให้ลบ")
	}

	rep := DeleteReport{Scope: scope, Items: []ItemResult{}}
	for _, name := range targets {
		if filepath.Base(name) != name || name == "." || name == ".." {
			rep.Items = append(rep.Items, ItemResult{Name: name, Status: "error", Message: "ชื่อไฟล์ไม่ถูกต้อง"})
			continue
		}
		full := filepath.Join(dir, name)
		if !fileExists(full) {
			if scope == "dest" {
				if rec, _ := s.store.FindImage(dest, name); rec != nil {
					_ = s.store.DeleteImage(dest, name)
					rep.Deleted++
					rep.Items = append(rep.Items, ItemResult{Name: name, Status: "deleted", Message: "ไม่พบไฟล์จริง — ลบเฉพาะประวัติใน DB"})
					continue
				}
			}
			rep.Items = append(rep.Items, ItemResult{Name: name, Status: "error", Message: "ไม่พบไฟล์"})
			continue
		}
		if !IsImageName(name) {
			rep.Items = append(rep.Items, ItemResult{Name: name, Status: "error", Message: "ไม่ใช่ไฟล์รูปภาพ (ไม่ลบเพื่อความปลอดภัย)"})
			continue
		}
		if err := os.Remove(full); err != nil {
			rep.Items = append(rep.Items, ItemResult{Name: name, Status: "error", Message: "ลบไม่สำเร็จ: " + err.Error()})
			continue
		}
		if scope == "dest" {
			_ = s.store.DeleteImage(dest, name)
		}
		rep.Deleted++
		rep.Items = append(rep.Items, ItemResult{Name: name, Status: "deleted", Message: "ลบแล้ว"})
	}
	return rep, nil
}

// ===================== ค้นหา =====================

// Search ค้นหารูปใน Destination ปัจจุบัน จากคำอธิบาย (หลายคำ = ต้องมีครบทุกคำ)
// และ/หรือ Tag (ต้องตรงกัน)
func (s *Service) Search(keyword, tag string) ([]Image, error) {
	_, dest := s.paths()
	if dest == "" {
		return nil, ErrNoDest
	}
	kws := strings.Fields(strings.ToLower(keyword))
	return s.store.Search(dest, kws, NormalizeTag(tag))
}

// TagCloud คืนจำนวนการใช้งานของแต่ละ Tag ใน Destination ปัจจุบัน
func (s *Service) TagCloud() ([]TagCount, error) {
	_, dest := s.paths()
	if dest == "" {
		return []TagCount{}, nil
	}
	return s.store.TagCounts(dest)
}

// ImagePath คืน path เต็มของรูป (ใช้เสิร์ฟรูปให้ UI)
func (s *Service) ImagePath(id uint) (string, error) {
	img, err := s.store.GetImage(id)
	if err != nil {
		return "", err
	}
	if img == nil {
		return "", errors.New("ไม่พบรูป")
	}
	return filepath.Join(img.Dest, img.Filename), nil
}

// ===================== helpers =====================

var imageExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true}

// IsImageName ตรวจนามสกุลแบบไม่สนตัวพิมพ์เล็ก/ใหญ่ (กล้อง/มือถือมักเป็น .JPG)
func IsImageName(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

func listImageFiles(dir string) ([]FileEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("อ่านโฟลเดอร์ไม่ได้: %w", err)
	}
	out := []FileEntry{}
	for _, e := range entries {
		if e.IsDir() || !IsImageName(e.Name()) || strings.HasPrefix(e.Name(), "._") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileEntry{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime().Format("2006-01-02 15:04")})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func countImages(dir string) (int, error) {
	files, err := listImageFiles(dir)
	return len(files), err
}

func resolveTargets(src string, names []string, all bool) ([]string, error) {
	var targets []string
	if all {
		files, err := listImageFiles(src)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			targets = append(targets, f.Name)
		}
		return targets, nil
	}
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		targets = append(targets, n)
	}
	// กัน path traversal: ต้องเป็นชื่อไฟล์เดี่ยว ๆ เท่านั้น
	for _, n := range targets {
		if filepath.Base(n) != n || n == "." || n == ".." {
			return nil, fmt.Errorf("ชื่อไฟล์ไม่ถูกต้อง: %s", n)
		}
	}
	return targets, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if ai, e1 := os.Stat(a); e1 == nil {
		if bi, e2 := os.Stat(b); e2 == nil {
			return os.SameFile(ai, bi)
		}
	}
	return a == b
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

// FormatElapsed แสดงเวลาเป็น ms ถ้าน้อยกว่า 1 วินาที มิฉะนั้นเป็น s
// (ตามที่งานกลางภาคกำหนด)
func FormatElapsed(d time.Duration) string {
	m := ms(d)
	if m >= 1000 {
		return fmt.Sprintf("%.2f s", m/1000)
	}
	return fmt.Sprintf("%.2f ms", m)
}
