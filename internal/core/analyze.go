package core

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Analysis คือผลวิเคราะห์ภาพ 1 รูป
type Analysis struct {
	Description string
	Tags        []string
	Width       int
	Height      int
	Analyzer    string // "local" | "ai"
	Note        string // ข้อความเตือน เช่น AI ล้มเหลวแล้วใช้ local แทน
}

// AnalyzeReport คือผลของการวิเคราะห์ใหม่
type AnalyzeReport struct {
	Total   int     `json:"total"`
	Done    int     `json:"done"`
	AIUsed  int     `json:"aiUsed"`
	Note    string  `json:"note"`
	Ms      float64 `json:"ms"`
	Elapsed string  `json:"elapsed"`
	Detail  string  `json:"detail"` // สรุปเวลาที่หมดไปกับ AI (ว่างถ้าไม่ได้ใช้ AI)
}

// analyzeLocalFile วิเคราะห์ภาพในเครื่อง (ใช้เมื่อไม่ได้เปิด AI หรือ AI ล้มเหลว)
// note = ข้อความเตือนที่จะแนบมา (เช่น เหตุผลที่ AI ล้มเหลว)
func analyzeLocalFile(path, note string) Analysis {
	local, lerr := AnalyzeLocal(path)
	if note != "" {
		local.Note = note
	}
	if lerr != nil {
		// อ่านภาพไม่ได้ (เช่น .webp/.bmp) — อย่างน้อยให้มีคำอธิบายจากชื่อไฟล์
		return Analysis{
			Description: fmt.Sprintf("ไฟล์รูปภาพ %s (วิเคราะห์เนื้อหาภาพไม่ได้: %v)", baseNoExt(path), lerr),
			Tags:        local.MetaTags,
			Analyzer:    "local",
			Note:        local.Note,
		}
	}
	return Analysis{Description: local.Description, Tags: local.Tags, Width: local.Width, Height: local.Height, Analyzer: "local", Note: local.Note}
}

type analyzeJob struct {
	id   uint
	path string
	name string
}

// runPool รัน fn(0..n-1) ด้วย worker จำนวนที่กำหนด
func runPool(workers, n int, fn func(i int)) {
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				fn(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

// analyzeIDs วิเคราะห์รูปตาม id แล้วบันทึกลง DB
//   - เปิด Vision AI: ส่งทีละชุด (AIBatchSize รูป/คำขอ) หลายชุดพร้อมกัน (AIConcurrency)
//     ผลเป็น "หมวดหมู่" จาก AICategories เท่านั้น; ชุดที่พังจะแบ่งครึ่งลองใหม่,
//     ถ้าโควตาเต็มจะหยุดยิง AI ที่เหลือทันทีแล้วใช้ตัววิเคราะห์ในเครื่องแทน
//   - ไม่เปิด AI: วิเคราะห์ในเครื่องแบบขนาน (AnalyzeWorkers)
func (s *Service) analyzeIDs(ids []uint) (done, aiUsed int, note, detail string) {
	if len(ids) == 0 {
		return
	}
	startAll := time.Now()
	s.mu.RLock()
	st := s.settings
	s.mu.RUnlock()
	useAI := st.AIEnabled && st.AIKey != ""

	var mu sync.Mutex
	counter := 0
	total := len(ids)

	report := func(name, status, n string, ai bool) {
		mu.Lock()
		counter++
		c := counter
		if status == "ok" {
			done++
			if ai {
				aiUsed++
			}
		}
		if n != "" && note == "" {
			note = n
		}
		aiNow := aiUsed
		mu.Unlock()
		s.emit(ProgressEvent{Phase: "analyze", Index: c, Total: total, Name: name, Status: status, AI: aiNow})
	}
	save := func(j analyzeJob, a Analysis) {
		status := "ok"
		tags := append([]string{"backup"}, a.Tags...)
		if err := s.store.UpdateAnalysis(j.id, a.Description, a.Analyzer, a.Width, a.Height, tags); err != nil {
			status = "error"
		}
		report(j.name, status, a.Note, a.Analyzer == "ai")
	}

	// ---- โหลดรายการรูป ----
	jobs := make([]analyzeJob, 0, len(ids))
	for _, id := range ids {
		img, err := s.store.GetImage(id)
		if err != nil || img == nil {
			report("", "error", "", false)
			continue
		}
		jobs = append(jobs, analyzeJob{id: id, path: joinPath(img.Dest, img.Filename), name: img.Filename})
	}

	if !useAI {
		workers := s.AnalyzeWorkers
		runPool(workers, len(jobs), func(i int) { save(jobs[i], analyzeLocalFile(jobs[i].path, "")) })
		return
	}

	// ---- Vision AI แบบชุด ----
	batchSize, conc := s.AIBatchSize, s.AIConcurrency
	if batchSize < 1 {
		batchSize = DefaultAIBatchSize
	}
	if conc < 1 {
		conc = DefaultAIConcurrency
	}
	stats := &AIStats{}
	budget := s.AIBudget
	if budget <= 0 {
		budget = DefaultAIBudget
	}
	deadline := startAll.Add(budget)
	cfg := AIConfig{APIKey: st.AIKey, Model: st.AIModel, BaseURL: s.AIBaseURL, Stats: stats, Gate: NewAIGate(conc), Deadline: deadline}
	picker := NewModelPicker(cfg)
	var quotaHit atomic.Bool
	fallback := func(j analyzeJob, why string) {
		save(j, analyzeLocalFile(j.path, "Vision AI ล้มเหลว (ใช้การวิเคราะห์ในเครื่องแทน): "+why))
	}
	var process func(js []analyzeJob)
	process = func(js []analyzeJob) {
		if !quotaHit.Load() && time.Now().After(deadline) {
			quotaHit.Store(true)
		}
		model := picker.Current()
		if quotaHit.Load() || model == "" {
			for _, j := range js {
				fallback(j, "โควตาเต็ม/เซิร์ฟเวอร์ยุ่งต่อเนื่อง/เกินเวลา "+budget.Round(time.Second).String())
			}
			return
		}
		paths := make([]string, len(js))
		for i, j := range js {
			paths[i] = j.path
		}
		bc := cfg
		bc.Model = model
		items, err := AnalyzeAIBatch(bc, paths)
		if err != nil {
			switch {
			case errors.Is(err, ErrAIBudget):
				quotaHit.Store(true) // หมดงบเวลา: ที่เหลือทั้งหมดใช้แบบในเครื่อง
				for _, j := range js {
					fallback(j, err.Error())
				}
			case errors.Is(err, ErrAIBusy), errors.Is(err, ErrAIQuota):
				// โมเดลนี้ติด 503/429 ต่อเนื่อง → ข้ามไปโมเดลสำรอง แล้วลองชุดเดิมใหม่
				if picker.Fail(model) != "" {
					process(js)
					return
				}
				for _, j := range js {
					fallback(j, err.Error()+" — ลองใหม่ภายหลัง หรือเปลี่ยนโมเดลในตั้งค่า")
				}
			case len(js) > 1: // ชุดนี้พัง (JSON เพี้ยน/ถูกบล็อก) → แบ่งครึ่งลองใหม่
				mid := len(js) / 2
				process(js[:mid])
				process(js[mid:])
			default:
				fallback(js[0], err.Error())
			}
			return
		}
		picker.MarkUsed(model)
		for k, it := range items {
			if it.err != nil {
				fallback(js[k], it.err.Error())
				continue
			}
			save(js[k], Analysis{Description: it.Description, Tags: it.Tags, Width: it.Width, Height: it.Height, Analyzer: "ai"})
		}
	}
	nBatches := (len(jobs) + batchSize - 1) / batchSize
	runPool(conc, nBatches, func(b int) {
		lo, hi := b*batchSize, (b+1)*batchSize
		if hi > len(jobs) {
			hi = len(jobs)
		}
		process(jobs[lo:hi])
	})
	detail = stats.Summary(time.Since(startAll))
	if detail != "" {
		detail += picker.UsedSummary()
	}
	return
}

// Reanalyze วิเคราะห์รูปใหม่ (ids ว่าง + all=true คือทุกรูปใน Destination)
// ใช้เมื่อเปิด Vision AI ภายหลัง หรืออยากให้คำอธิบายใหม่
func (s *Service) Reanalyze(ids []uint, all bool) (AnalyzeReport, error) {
	_, dest := s.paths()
	if dest == "" {
		return AnalyzeReport{}, ErrNoDest
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if all {
		recs, err := s.store.ListImages(dest)
		if err != nil {
			return AnalyzeReport{}, err
		}
		ids = ids[:0]
		for _, r := range recs {
			if fileExists(joinPath(r.Dest, r.Filename)) {
				ids = append(ids, r.ID)
			}
		}
	}
	if len(ids) == 0 {
		return AnalyzeReport{}, errors.New("ไม่มีรูปให้วิเคราะห์")
	}
	start := time.Now()
	done, ai, note, detail := s.analyzeIDs(ids)
	return AnalyzeReport{Total: len(ids), Done: done, AIUsed: ai, Note: note, Detail: detail, Ms: ms(time.Since(start)), Elapsed: FormatElapsed(time.Since(start))}, nil
}

// ReanalyzePending วิเคราะห์เฉพาะรูปที่ AI ยังไม่ได้ทำ (Analyzer != "ai")
// ใช้ต่อจากรอบที่ AI ติด 503/หมดเวลา — ไม่ต้องจ่าย request ซ้ำกับรูปที่ได้หมวดหมู่จาก AI แล้ว
func (s *Service) ReanalyzePending() (AnalyzeReport, error) {
	_, dest := s.paths()
	if dest == "" {
		return AnalyzeReport{}, ErrNoDest
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	recs, err := s.store.ListImages(dest)
	if err != nil {
		return AnalyzeReport{}, err
	}
	ids := []uint{}
	for _, r := range recs {
		if r.Analyzer != "ai" && fileExists(joinPath(r.Dest, r.Filename)) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return AnalyzeReport{}, errors.New("ทุกรูปถูกวิเคราะห์ด้วย AI แล้ว")
	}
	start := time.Now()
	done, ai, note, detail := s.analyzeIDs(ids)
	return AnalyzeReport{Total: len(ids), Done: done, AIUsed: ai, Note: note, Detail: detail, Ms: ms(time.Since(start)), Elapsed: FormatElapsed(time.Since(start))}, nil
}

// ===================== Tag helpers =====================

// NormalizeTag ทำให้ tag อยู่ในรูปมาตรฐาน: ตัด # และช่องว่าง, ตัวพิมพ์เล็ก
func NormalizeTag(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	t = strings.TrimLeft(t, "#")
	t = strings.Join(strings.Fields(t), " ")
	if utf8.RuneCountInString(t) > 30 {
		r := []rune(t)
		t = string(r[:30])
	}
	return t
}

// NormalizeTags ทำความสะอาดรายการ tag: ตัดซ้ำ ตัดว่าง จำกัดไม่เกิน 20 tag
func NormalizeTags(tags []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range tags {
		n := NormalizeTag(t)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		if len(out) >= 20 {
			break
		}
	}
	return out
}
