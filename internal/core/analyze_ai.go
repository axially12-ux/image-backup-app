package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultAIModel คือโมเดลเริ่มต้นสำหรับ Vision AI (เปลี่ยนได้ในหน้าตั้งค่า)
// gemini-3.1-flash-lite เป็นรุ่น stable ที่มี free tier (รุ่น 2.5 ถูกปิดบริการแล้ว)
const DefaultAIModel = "gemini-3.1-flash-lite"

// defaultGeminiBaseURL คือ endpoint ของ Gemini API
const defaultGeminiBaseURL = "https://generativelanguage.googleapis.com"

// ค่าปรับความเร็ว/โควตา
//   - free tier จำกัดเป็น "จำนวน request ต่อวัน" (~500) ไม่ใช่จำนวนรูป
//     จึงส่งหลายรูปต่อ 1 request: 1,000 รูป ÷ 16 ≈ 63 request เท่านั้น
//   - ย่อรูปเหลือ 768px พอสำหรับจัดหมวดหมู่ → อัปโหลดเร็ว กิน token น้อย
const (
	DefaultAIBatchSize   = 16              // จำนวนรูปต่อ 1 request (ยิ่งมาก request ยิ่งน้อย → ชนเพดานต่อนาทีน้อยลง)
	DefaultAIConcurrency = 8               // จำนวน request ที่ยิงพร้อมกัน
	aiImageMaxSide       = 768             // ด้านยาวสุดของรูปที่ส่งให้ AI
	DefaultAIBudget      = 6 * time.Minute // เวลาสูงสุดที่ให้ AI; เกินแล้วที่เหลือใช้แบบในเครื่อง (ให้ทั้งงานจบใน ~10 นาที)
)

// AIConfig คือค่าที่ใช้เรียก Gemini generateContent API
type AIConfig struct {
	APIKey  string
	Model   string
	BaseURL string   // ค่าว่าง = https://generativelanguage.googleapis.com (ใช้ทดสอบ)
	Stats   *AIStats // ไม่บังคับ: เก็บสถิติเวลาไว้แสดงในรายงาน
	Gate    *AIGate  // ไม่บังคับ: คุมจำนวน request พร้อมกันแบบปรับตัวเอง
	// Deadline (ไม่บังคับ): เลยเวลานี้แล้วไม่รอ/ไม่ลองซ้ำ คืน ErrAIBudget ทันที
	Deadline time.Time
}

// AIStats เก็บว่าเวลาหมดไปกับอะไร (ปลอดภัยต่อการเรียกพร้อมกัน)
type AIStats struct {
	Requests   atomic.Int64 // request ที่สำเร็จ
	APINs      atomic.Int64 // เวลารวมที่รอ API ตอบ (รวมทุก request ที่ยิงพร้อมกัน)
	PrepNs     atomic.Int64 // เวลาเตรียมรูป (นับตามเวลาจริงของแต่ละชุด)
	Throttles  atomic.Int64 // จำนวนครั้งที่โดน 429/503 แล้วต้องรอ
	ThrottleNs atomic.Int64 // เวลารวมที่รอเพราะโดนจำกัด
}

// Summary สรุปเป็นข้อความให้ผู้ใช้ดู (wall = เวลาจริงที่ใช้ทั้งหมดของการวิเคราะห์)
func (st *AIStats) Summary(wall time.Duration) string {
	n := st.Requests.Load()
	if n == 0 && st.Throttles.Load() == 0 {
		return ""
	}
	avg := time.Duration(0)
	if n > 0 {
		avg = time.Duration(st.APINs.Load() / n)
	}
	return fmt.Sprintf("AI %d request · API ตอบเฉลี่ย %.1f วิ/request · เตรียมรูปรวม %.1f วิ · โดนจำกัดความเร็ว %d ครั้ง (รอรวม %.0f วิ) · เวลาจริง %.1f วิ",
		n, avg.Seconds(), time.Duration(st.PrepNs.Load()).Seconds(), st.Throttles.Load(), time.Duration(st.ThrottleNs.Load()).Seconds(), wall.Seconds())
}

// NormalizeAIModel กันกรณีในฐานข้อมูลยังเก็บชื่อโมเดลเก่า (เช่น claude-...) ไว้
func NormalizeAIModel(m string) string {
	m = strings.TrimSpace(m)
	if m == "" || strings.HasPrefix(strings.ToLower(m), "claude") {
		return DefaultAIModel
	}
	return strings.TrimPrefix(m, "models/")
}

// AIResult คือผลที่ AI ส่งกลับสำหรับ 1 รูป (Tags = หมวดหมู่จาก AICategories เท่านั้น)
type AIResult struct {
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	// Width/Height ของไฟล์ต้นฉบับ (0 ถ้าไม่ทราบ)
	Width, Height int `json:"-"`
}

// prepSem จำกัดจำนวนรูปที่ถอดรหัส/ย่อพร้อมกัน = จำนวน core (กันกินแรม/CPU เกิน)
var prepSem = make(chan struct{}, max(2, runtime.NumCPU()))

// aiThinkingOff: ส่ง thinkingConfig เพื่อปิด "การคิดก่อนตอบ" (ช้าและเปลืองโดยไม่จำเป็นสำหรับงานจัดหมวด)
// ถ้า API ปฏิเสธพารามิเตอร์นี้ (400) จะปิดตัวเลือกแล้วลองใหม่โดยไม่ส่ง
var aiThinkingOff atomic.Bool

func init() { aiThinkingOff.Store(true) }

// ErrAIBudget แปลว่าใช้เวลากับ AI เกินงบที่กำหนดแล้ว (ที่เหลือใช้แบบในเครื่อง)
var ErrAIBudget = errors.New("เกินเวลาที่กำหนดสำหรับ AI")

// ErrAIBusy แปลว่าเซิร์ฟเวอร์ Gemini ยุ่ง/โอเวอร์โหลด (503) แม้ลองใหม่หลายรอบ — ไม่ใช่โควตาของเรา
var ErrAIBusy = errors.New("เซิร์ฟเวอร์ Gemini ยุ่ง (503)")

// AIGate คุมจำนวน request ที่ยิงพร้อมกันแบบปรับตัวเอง: โดน 429 (จำกัดความเร็ว) ลดครึ่งหนึ่งทันที
// แล้วค่อยเพิ่มกลับทีละ 1 เมื่อสำเร็จติดกัน 3 ครั้ง
type AIGate struct {
	mu       sync.Mutex
	cond     *sync.Cond
	cap, max int
	inflight int
	okRun    int
}

func NewAIGate(n int) *AIGate {
	g := &AIGate{cap: n, max: n}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *AIGate) Acquire() {
	g.mu.Lock()
	for g.inflight >= g.cap {
		g.cond.Wait()
	}
	g.inflight++
	g.mu.Unlock()
}

func (g *AIGate) Release(overloaded, success bool) {
	g.mu.Lock()
	g.inflight--
	switch {
	case overloaded:
		g.cap, g.okRun = max(1, g.cap/2), 0
	case success:
		if g.okRun++; g.okRun >= 3 && g.cap < g.max {
			g.cap++
			g.okRun = 0
		}
	}
	g.cond.Broadcast()
	g.mu.Unlock()
}

// Cap คือจำนวนที่ยิงพร้อมกันได้ ณ ตอนนี้ (ใช้แสดงในรายงาน)
func (g *AIGate) Cap() int { g.mu.Lock(); defer g.mu.Unlock(); return g.cap }

// ErrAIQuota แปลว่าติดเพดานโควตา/คำขอ (429/503) แม้ลองใหม่แล้ว — ไม่ควรยิงต่อ
var ErrAIQuota = errors.New("โควตา Gemini เต็ม/ถูกจำกัดความเร็ว")

// จำนวนครั้งที่ลองเรียก API เมื่อติดเพดาน (429/503) และเวลารอพื้นฐาน (ปรับได้ตอนทดสอบ)
var (
	aiMaxAttempts  = 5 // 429: รอรวม ~40 วินาที พอให้เพดานต่อนาทีรีเซ็ต
	aiBusyAttempts = 6 // 503: ลองได้มากกว่า เพราะมักหายเองใน 1 นาที
	aiRetryDelay   = 4 * time.Second
)

// AICategories คือรายการหมวดหมู่ที่ AI เลือกได้ (บังคับทั้งใน prompt, schema และตอนตรวจผล)
// แก้รายการนี้ที่เดียวเพื่อเพิ่ม/ลดหมวด
var AICategories = []string{
	// สถานที่ / ธรรมชาติ
	"ทะเล", "ชายหาด", "เกาะ", "ภูเขา", "น้ำตก", "แม่น้ำ/ทะเลสาบ", "ป่า", "ทุ่งนา/ไร่สวน", "ธรรมชาติ",
	// ท้องฟ้า / เวลา
	"ท้องฟ้า", "พระอาทิตย์ขึ้น/ตก", "กลางคืน",
	// เมือง / สถานที่
	"เมือง", "อาคาร", "ถนน", "สวนสาธารณะ", "วัด/ศาสนสถาน", "แลนด์มาร์ก", "ในอาคาร",
	// คน
	"ผู้หญิง", "ผู้ชาย", "เด็ก", "ทารก", "ผู้สูงอายุ", "กลุ่มคน", "เซลฟี่", "ภาพบุคคล", "ครอบครัว/คู่รัก",
	// กิจกรรม
	"งานเลี้ยง/กิจกรรม", "งานแต่งงาน", "รับปริญญา", "กีฬา", "ดนตรี/คอนเสิร์ต", "ท่องเที่ยว",
	// สัตว์ / พืช
	"สัตว์", "แมว", "สุนัข", "นก", "สัตว์น้ำ", "ดอกไม้", "ต้นไม้",
	// อาหาร
	"อาหาร", "เครื่องดื่ม", "ผลไม้", "ขนม/ของหวาน",
	// ยานพาหนะ
	"รถยนต์", "มอเตอร์ไซค์/จักรยาน", "เรือ", "เครื่องบิน", "ยานพาหนะ",
	// อื่น ๆ
	"สิ่งของ", "เอกสาร/ข้อความ", "สกรีนช็อต", "ศิลปะ/ภาพวาด", "อื่นๆ",
}

const aiMaxCategories = 5 // หมวดต่อรูปไม่เกินนี้ เพื่อไม่ให้แท็กบวม

const aiFallbackCategory = "อื่นๆ"

var aiCategorySet = func() map[string]string {
	m := map[string]string{}
	for _, c := range AICategories {
		m[NormalizeTag(c)] = c
	}
	return m
}()

// NormalizeCategories เก็บเฉพาะหมวดที่อยู่ในรายการ (ตัดซ้ำ/คำนอกรายการ) สูงสุด aiMaxCategories
// ถ้าไม่เหลือเลยให้เป็น "อื่นๆ"
func NormalizeCategories(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range in {
		c, ok := aiCategorySet[NormalizeTag(t)]
		if !ok || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		if len(out) >= aiMaxCategories {
			break
		}
	}
	if len(out) == 0 {
		out = append(out, aiFallbackCategory)
	}
	return out
}

func aiPrompt(n int) string {
	return fmt.Sprintf(`ด้านบนคือภาพ %d ภาพ เรียงตามลำดับ (ป้ายกำกับ "รูปที่ 0" ถึง "รูปที่ %d") จัดหมวดหมู่และอธิบายทุกภาพ
ตอบเป็น JSON เท่านั้น: {"results":[{"i":0,"description":"...","categories":["..."]}, ...]} ต้องมีครบทั้ง %d ภาพ
- i: หมายเลขรูปตามป้ายกำกับ
- description: ภาษาไทย 1 ประโยคสั้น ๆ บอกว่าในภาพมีอะไร อยู่ที่ไหน
- categories: เลือก 1-%d หมวดที่ตรงที่สุด จากรายการนี้เท่านั้น (ห้ามสร้างหมวดใหม่): %s
  กติกา: เลือกหมวดที่ "เจาะจงที่สุด" ก่อน และไม่ต้องใส่หมวดกว้างซ้ำถ้ามีหมวดเจาะจงแล้ว (เช่น เห็นแมว ใช้ "แมว" ไม่ต้องใส่ "สัตว์" ซ้ำ; เห็นชายหาด ใช้ "ชายหาด" แล้วเสริม "ทะเล" ได้ถ้าเห็นทะเลชัด)
  ผู้หญิง/ผู้ชาย/เด็ก/ทารก/ผู้สูงอายุ ใช้เมื่อเห็นคนเด่นชัดในภาพ, ถ้ามีหลายคนให้ใช้ "กลุ่มคน" (หรือ "ครอบครัว/คู่รัก" ถ้าเห็นชัดว่าเป็นครอบครัวหรือคู่รัก)
  ใช้ "อื่นๆ" เฉพาะเมื่อไม่มีหมวดไหนตรงเลย`,
		n, n-1, n, aiMaxCategories, strings.Join(AICategories, ", "))
}

// AnalyzeAI วิเคราะห์ 1 รูป (ครอบ AnalyzeAIBatch)
func AnalyzeAI(cfg AIConfig, path string) (AIResult, error) {
	res, err := AnalyzeAIBatch(cfg, []string{path})
	if err != nil {
		return AIResult{}, err
	}
	if res[0].err != nil {
		return AIResult{}, res[0].err
	}
	return res[0].AIResult, nil
}

// BatchItem คือผลของรูปหนึ่งในชุด (err != nil = รูปนี้ไม่ได้ผลจาก AI)
type BatchItem struct {
	AIResult
	err error
}

// AnalyzeAIBatch ส่งหลายรูปใน request เดียว แล้วได้ผลกลับตามลำดับเดิม
// - error ระดับ request (เครือข่าย/โควตา/JSON พัง) → คืน error (ErrAIQuota ถ้าติดเพดาน)
// - รูปที่เตรียมส่งไม่ได้ หรือ AI ตอบไม่ครบ → BatchItem.err ของรูปนั้น
func AnalyzeAIBatch(cfg AIConfig, paths []string) ([]BatchItem, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("ไม่มี API Key")
	}
	if len(paths) == 0 {
		return nil, errors.New("ไม่มีรูป")
	}
	cfg.Model = NormalizeAIModel(cfg.Model)
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultGeminiBaseURL
	}

	items := make([]BatchItem, len(paths))
	// เตรียมรูป (ถอดรหัส+ย่อ) แบบขนาน — เป็นงาน CPU ที่ช้าสำหรับรูปมือถือ 12MP+
	type prepared struct {
		mt, b64 string
		w, h    int
		err     error
	}
	prep := make([]prepared, len(paths))
	prepStart := time.Now()
	var pwg sync.WaitGroup
	for i, p := range paths {
		pwg.Add(1)
		go func() {
			defer pwg.Done()
			prepSem <- struct{}{}
			defer func() { <-prepSem }()
			prep[i].mt, prep[i].b64, prep[i].w, prep[i].h, prep[i].err = prepareImageForAI(p)
		}()
	}
	pwg.Wait()
	if cfg.Stats != nil {
		cfg.Stats.PrepNs.Add(int64(time.Since(prepStart)))
	}
	parts := []any{}
	sent := []int{} // index ของรูปที่ส่งจริง → ตำแหน่งในคำขอ = ลำดับใน sent
	for i := range paths {
		if prep[i].err != nil {
			items[i].err = prep[i].err
			continue
		}
		items[i].Width, items[i].Height = prep[i].w, prep[i].h
		parts = append(parts,
			map[string]any{"text": fmt.Sprintf("รูปที่ %d:", len(sent))},
			map[string]any{"inlineData": map[string]any{"mimeType": prep[i].mt, "data": prep[i].b64}},
		)
		sent = append(sent, i)
	}
	if len(sent) == 0 {
		return items, nil
	}
	parts = append(parts, map[string]any{"text": aiPrompt(len(sent))})

	catEnum := make([]any, len(AICategories))
	for i, c := range AICategories {
		catEnum[i] = c
	}
	body := map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": parts}},
		"generationConfig": map[string]any{
			// ~120 token ต่อรูป + เผื่อ thinking ของโมเดลบางรุ่น
			"maxOutputTokens":  1024 + 120*len(sent),
			"responseMimeType": "application/json",
			// บังคับรูปแบบ + หมวดหมู่ที่อนุญาต (ตรวจซ้ำฝั่งเราอีกชั้นใน NormalizeCategories)
			"responseSchema": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"results": map[string]any{
						"type": "ARRAY",
						"items": map[string]any{
							"type": "OBJECT",
							"properties": map[string]any{
								"i":           map[string]any{"type": "INTEGER"},
								"description": map[string]any{"type": "STRING"},
								"categories": map[string]any{
									"type":  "ARRAY",
									"items": map[string]any{"type": "STRING", "enum": catEnum},
								},
							},
							"required": []any{"i", "description", "categories"},
						},
					},
				},
				"required": []any{"results"},
			},
		},
	}
	genCfg := body["generationConfig"].(map[string]any)
	buildPayload := func() []byte {
		if aiThinkingOff.Load() {
			genCfg["thinkingConfig"] = map[string]any{"thinkingLevel": "minimal"}
		} else {
			delete(genCfg, "thinkingConfig")
		}
		b, _ := json.Marshal(body)
		return b
	}
	payload := buildPayload()
	url := strings.TrimRight(cfg.BaseURL, "/") + "/v1beta/models/" + cfg.Model + ":generateContent"

	var data []byte
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-goog-api-key", cfg.APIKey)

		// หลายรูปต่อ request ใช้เวลานานกว่าเดิม จึงให้ timeout 90 วินาที
		if cfg.Gate != nil {
			cfg.Gate.Acquire()
		}
		callStart := time.Now()
		resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
		if err != nil {
			if cfg.Gate != nil {
				cfg.Gate.Release(false, false)
			}
			return nil, fmt.Errorf("เชื่อมต่อ API ไม่ได้: %w", err)
		}
		data, _ = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if cfg.Gate != nil {
			// ลดจำนวนที่ยิงพร้อมกันเฉพาะตอนโดนจำกัดความเร็ว (429) — 503 เป็นเรื่องฝั่งเซิร์ฟเวอร์ ลดแล้วไม่ช่วย มีแต่ช้าลง
			cfg.Gate.Release(resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusOK)
		}

		if resp.StatusCode == http.StatusOK {
			if cfg.Stats != nil {
				cfg.Stats.Requests.Add(1)
				cfg.Stats.APINs.Add(int64(time.Since(callStart)))
			}
			break
		}
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		// โมเดลบางรุ่นไม่รับ thinkingConfig → ปิดตัวเลือกนี้แล้วลองใหม่ทันที (ไม่นับเป็นครั้งที่ลอง)
		if resp.StatusCode == http.StatusBadRequest && aiThinkingOff.Load() && strings.Contains(strings.ToLower(msg), "thinking") {
			aiThinkingOff.Store(false)
			payload = buildPayload()
			attempt--
			continue
		}
		busy := resp.StatusCode == http.StatusServiceUnavailable
		throttled := resp.StatusCode == http.StatusTooManyRequests
		// โควตารายวันหมด: รอแล้วก็ไม่หาย → ไม่ต้องลองซ้ำ ให้ผู้เรียกหยุดยิงทันที
		lm := strings.ToLower(msg)
		daily := strings.Contains(lm, "perday") || strings.Contains(lm, "per day")
		limit := aiMaxAttempts
		if busy {
			limit = aiBusyAttempts
		}
		if (throttled || busy) && !daily && attempt < limit {
			wait := time.Duration(attempt) * aiRetryDelay
			if busy { // 503: backoff แบบเอกซ์โปเนนเชียล + สุ่มเล็กน้อย ไม่ให้ทุก worker กลับมาพร้อมกัน
				wait = aiRetryDelay<<(attempt-1) + time.Duration(rand.Int63n(int64(aiRetryDelay)))
				if wait > 12*time.Second { // 503 สุ่มเกิดเป็นรายคำขอ: ลองถี่ขึ้นมีโอกาสผ่านเร็วกว่ารอนาน
					wait = 12 * time.Second
				}
			}
			if ra, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && ra > 0 && ra <= 60 {
				wait = time.Duration(ra) * time.Second
			}
			if !cfg.Deadline.IsZero() && time.Now().Add(wait).After(cfg.Deadline) {
				return nil, fmt.Errorf("%w (ติด %d ระหว่างรอ)", ErrAIBudget, resp.StatusCode)
			}
			if cfg.Stats != nil {
				cfg.Stats.Throttles.Add(1)
				cfg.Stats.ThrottleNs.Add(int64(wait))
			}
			time.Sleep(wait)
			continue
		}
		err = fmt.Errorf("API ตอบกลับ %d: %s", resp.StatusCode, truncate(msg, 200))
		switch {
		case busy:
			err = fmt.Errorf("%w (%v)", ErrAIBusy, err)
		case throttled:
			err = fmt.Errorf("%w (%v)", ErrAIQuota, err)
		}
		return nil, err
	}

	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("อ่านคำตอบ API ไม่ได้: %w", err)
	}
	if len(out.Candidates) == 0 {
		if out.PromptFeedback.BlockReason != "" {
			return nil, fmt.Errorf("Gemini ปฏิเสธรูปชุดนี้ (%s)", out.PromptFeedback.BlockReason)
		}
		return nil, errors.New("Gemini ไม่ได้ส่งคำตอบกลับมา")
	}
	var text string
	for _, p := range out.Candidates[0].Content.Parts {
		if !p.Thought {
			text += p.Text
		}
	}
	results, err := parseAIBatchJSON(text)
	if err != nil {
		return nil, err
	}
	got := map[int]AIResult{}
	for _, r := range results {
		got[r.I] = AIResult{Description: strings.TrimSpace(r.Description), Tags: NormalizeCategories(r.Categories)}
	}
	for pos, idx := range sent {
		r, ok := got[pos]
		if !ok || r.Description == "" {
			items[idx].err = errors.New("AI ตอบไม่ครบสำหรับรูปนี้")
			continue
		}
		r.Width, r.Height = items[idx].Width, items[idx].Height
		items[idx].AIResult = r
	}
	return items, nil
}

type aiBatchRow struct {
	I           int      `json:"i"`
	Description string   `json:"description"`
	Categories  []string `json:"categories"`
}

// parseAIBatchJSON ดึง JSON ออกจากคำตอบ (กันกรณีโมเดลใส่ ```json ครอบมา)
// รับทั้ง {"results":[...]} และ [...] ตรง ๆ
func parseAIBatchJSON(text string) ([]aiBatchRow, error) {
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if a, b := strings.Index(text, "["), strings.LastIndex(text, "]"); a >= 0 && (i < 0 || a < i) {
		i, j = a, b
	}
	if i < 0 || j <= i {
		return nil, errors.New("คำตอบของ AI ไม่ใช่ JSON")
	}
	raw := []byte(text[i : j+1])
	var wrapped struct {
		Results []aiBatchRow `json:"results"`
	}
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &wrapped.Results); err != nil {
			return nil, fmt.Errorf("แปลง JSON ของ AI ไม่ได้: %w", err)
		}
	} else if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("แปลง JSON ของ AI ไม่ได้: %w", err)
	}
	return wrapped.Results, nil
}

// prepareImageForAI ย่อ/แปลงภาพให้เหมาะกับ API (<= aiImageMaxSide px, JPEG) ถ้าถอดรหัสไม่ได้
// (เช่น webp) และไฟล์ไม่ใหญ่เกิน 4MB จะส่งไฟล์ต้นฉบับ
// คืนขนาดภาพต้นฉบับ (w,h) ด้วย เพื่อไม่ต้องถอดรหัสซ้ำอีกรอบตอนบันทึก
func prepareImageForAI(path string) (mediaType, b64 string, w, h int, err error) {
	if f, ferr := os.Open(path); ferr == nil {
		if cfg, _, cerr := image.DecodeConfig(f); cerr == nil {
			w, h = cfg.Width, cfg.Height
		}
		f.Close()
	}
	img, derr := decodeFile(path)
	if derr == nil {
		img = orient(scaleDown(img, aiImageMaxSide), ReadExif(path).Orientation)
		data, err := encodeJPEG(img, 80)
		if err != nil {
			return "", "", 0, 0, err
		}
		return "image/jpeg", base64.StdEncoding.EncodeToString(data), w, h, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", 0, 0, err
	}
	mt := http.DetectContentType(raw)
	if (mt != "image/webp" && mt != "image/gif" && mt != "image/png" && mt != "image/jpeg") || len(raw) > 4<<20 {
		return "", "", 0, 0, fmt.Errorf("ส่งรูปชนิดนี้ให้ AI ไม่ได้ (%s): %v", mt, derr)
	}
	return mt, base64.StdEncoding.EncodeToString(raw), w, h, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ListGeminiModels ดึงรายชื่อโมเดลที่ใช้ generateContent ได้และเป็นตระกูล flash (รองรับรูป) สำหรับใช้เป็นตัวสำรอง
// ตัดรุ่นที่ไม่ใช่งานวิเคราะห์ภาพทิ้ง (image/live/tts/audio/embedding/robotics/...) และเรียง flash-lite ก่อน
func ListGeminiModels(cfg AIConfig) ([]string, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultGeminiBaseURL
	}
	req, err := http.NewRequest("GET", strings.TrimRight(cfg.BaseURL, "/")+"/v1beta/models?pageSize=200", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", cfg.APIKey)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ListModels ตอบกลับ %d", resp.StatusCode)
	}
	var out struct {
		Models []struct {
			Name    string   `json:"name"`
			Methods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	skip := []string{"image", "live", "tts", "audio", "embed", "robotics", "computer", "customtools", "dialog", "native"}
	var lite, other []string
	for _, m := range out.Models {
		name := strings.TrimPrefix(m.Name, "models/")
		ln := strings.ToLower(name)
		if !strings.Contains(ln, "gemini") || !strings.Contains(ln, "flash") {
			continue
		}
		ok := false
		for _, me := range m.Methods {
			ok = ok || me == "generateContent"
		}
		for _, k := range skip {
			ok = ok && !strings.Contains(ln, k)
		}
		if !ok {
			continue
		}
		if strings.Contains(ln, "lite") {
			lite = append(lite, name)
		} else {
			other = append(other, name)
		}
	}
	return append(lite, other...), nil
}

// ModelPicker เลือกโมเดลสำรองอัตโนมัติ: โมเดลที่ติด 503/429 ต่อเนื่องจะถูกข้าม แล้วใช้ตัวถัดไป
// (ดึงรายชื่อจาก ListModels ครั้งเดียว ตอนที่โมเดลแรกล้มเหลวเป็นครั้งแรก)
type ModelPicker struct {
	discMu     sync.Mutex
	mu         sync.Mutex
	cfg        AIConfig
	list       []string
	bad        map[string]bool
	discovered bool
	used       []string
}

const maxFallbackModels = 3

func NewModelPicker(cfg AIConfig) *ModelPicker {
	return &ModelPicker{cfg: cfg, list: []string{NormalizeAIModel(cfg.Model)}, bad: map[string]bool{}}
}

func (p *ModelPicker) next() string {
	for _, m := range p.list {
		if !p.bad[m] {
			return m
		}
	}
	return ""
}

// Current คือโมเดลที่ควรใช้ตอนนี้ ("" = ไม่เหลือแล้ว)
func (p *ModelPicker) Current() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.next()
}

// Fail ทำเครื่องหมายว่าโมเดลนี้ใช้ไม่ได้ แล้วคืนโมเดลถัดไป ("" = หมดแล้ว)
func (p *ModelPicker) Fail(model string) string {
	p.mu.Lock()
	p.bad[model] = true
	p.mu.Unlock()

	// ดึงรายชื่อสำรองครั้งเดียว; worker อื่นที่ล้มพร้อมกันต้อง "รอ" จนดึงเสร็จ (ไม่งั้นจะเห็นรายชื่อว่างแล้วหนีไปใช้แบบในเครื่อง)
	p.discMu.Lock()
	if !p.discovered {
		names, _ := ListGeminiModels(p.cfg) // ล้มเหลวก็ไม่เป็นไร แค่ไม่มีตัวสำรอง
		p.mu.Lock()
		added := 0
		for _, n := range names {
			if added >= maxFallbackModels {
				break
			}
			dup := false
			for _, m := range p.list {
				dup = dup || m == n
			}
			if !dup {
				p.list = append(p.list, n)
				added++
			}
		}
		p.discovered = true
		p.mu.Unlock()
	}
	p.discMu.Unlock()
	return p.Current()
}

// MarkUsed จดว่าโมเดลนี้ตอบสำเร็จ (ไว้แสดงในรายงาน)
func (p *ModelPicker) MarkUsed(model string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.used {
		if m == model {
			return
		}
	}
	p.used = append(p.used, model)
}

// UsedSummary เช่น " · โมเดล: a(สำรอง) b" — ว่างถ้าใช้โมเดลหลักตัวเดียว
func (p *ModelPicker) UsedSummary() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.used) == 0 || (len(p.used) == 1 && p.used[0] == p.list[0]) {
		return ""
	}
	return " · โมเดลที่ใช้: " + strings.Join(p.used, ", ")
}
