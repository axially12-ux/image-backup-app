package core

import (
	"errors"
	"fmt"
	"strings"
	"sync"
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
}

// analyzeFile วิเคราะห์ภาพ: ถ้าเปิด Vision AI จะใช้ AI (และเสริม tag จาก
// EXIF/ขนาดภาพ) ถ้า AI ล้มเหลวหรือไม่ได้เปิด จะใช้ตัววิเคราะห์ในเครื่อง (local)
func (s *Service) analyzeFile(path string) Analysis {
	s.mu.RLock()
	st := s.settings
	s.mu.RUnlock()

	local, lerr := AnalyzeLocal(path)

	if st.AIEnabled && st.AIKey != "" {
		ai, err := AnalyzeAI(AIConfig{APIKey: st.AIKey, Model: st.AIModel, BaseURL: s.AIBaseURL}, path)
		if err == nil {
			out := Analysis{Description: ai.Description, Tags: ai.Tags, Width: local.Width, Height: local.Height, Analyzer: "ai"}
			out.Tags = append(out.Tags, local.MetaTags...)
			return out
		}
		local.Note = "Vision AI ล้มเหลว (ใช้การวิเคราะห์ในเครื่องแทน): " + err.Error()
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

// analyzeIDs วิเคราะห์รูปตาม id แบบขนาน (จำกัดจำนวน worker) แล้วบันทึกลง DB
func (s *Service) analyzeIDs(ids []uint) (done, aiUsed int, note string) {
	if len(ids) == 0 {
		return 0, 0, ""
	}
	workers := s.AnalyzeWorkers
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan [2]int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	counter := 0

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				id := ids[j[0]]
				img, err := s.store.GetImage(id)
				status := "ok"
				if err != nil || img == nil {
					status = "error"
				} else {
					a := s.analyzeFile(joinPath(img.Dest, img.Filename))
					tags := append([]string{"backup"}, a.Tags...)
					if err := s.store.UpdateAnalysis(id, a.Description, a.Analyzer, a.Width, a.Height, tags); err != nil {
						status = "error"
					}
					mu.Lock()
					if a.Analyzer == "ai" {
						aiUsed++
					}
					if a.Note != "" && note == "" {
						note = a.Note
					}
					mu.Unlock()
				}
				mu.Lock()
				counter++
				c := counter
				if status == "ok" {
					done++
				}
				mu.Unlock()
				name := ""
				if img != nil {
					name = img.Filename
				}
				s.emit(ProgressEvent{Phase: "analyze", Index: c, Total: len(ids), Name: name, Status: status})
			}
		}()
	}
	for i := range ids {
		jobs <- [2]int{i, 0}
	}
	close(jobs)
	wg.Wait()
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
	done, ai, note := s.analyzeIDs(ids)
	return AnalyzeReport{Total: len(ids), Done: done, AIUsed: ai, Note: note, Ms: ms(time.Since(start)), Elapsed: FormatElapsed(time.Since(start))}, nil
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
