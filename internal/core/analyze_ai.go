package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultAIModel คือโมเดลเริ่มต้นสำหรับ Vision AI (เปลี่ยนได้ในหน้าตั้งค่า)
// gemini-3.1-flash-lite เป็นรุ่น stable ที่มี free tier (รุ่น 2.5 ถูกปิดบริการแล้ว)
const DefaultAIModel = "gemini-3.1-flash-lite"

// defaultGeminiBaseURL คือ endpoint ของ Gemini API
const defaultGeminiBaseURL = "https://generativelanguage.googleapis.com"

// AIConfig คือค่าที่ใช้เรียก Gemini generateContent API
type AIConfig struct {
	APIKey  string
	Model   string
	BaseURL string // ค่าว่าง = https://generativelanguage.googleapis.com (ใช้ทดสอบ)
}

// NormalizeAIModel กันกรณีในฐานข้อมูลยังเก็บชื่อโมเดลเก่า (เช่น claude-...) ไว้
func NormalizeAIModel(m string) string {
	m = strings.TrimSpace(m)
	if m == "" || strings.HasPrefix(strings.ToLower(m), "claude") {
		return DefaultAIModel
	}
	return strings.TrimPrefix(m, "models/")
}

// AIResult คือผลที่ AI ส่งกลับ
type AIResult struct {
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// จำนวนครั้งที่ลองเรียก API เมื่อติดเพดาน (429/503) และเวลารอพื้นฐาน (ปรับได้ตอนทดสอบ)
var (
	aiMaxAttempts = 3
	aiRetryDelay  = 4 * time.Second
)

const aiPrompt = `วิเคราะห์ภาพนี้แล้วตอบเป็น JSON เท่านั้น (ห้ามมีข้อความอื่น):
{"description": "...", "tags": ["...", "..."]}
- description: คำอธิบายภาพเป็นภาษาไทย 1-2 ประโยค บอกว่าในภาพมีอะไร ทำอะไร อยู่ที่ไหน บรรยากาศเป็นอย่างไร
- tags: 5-10 คำสั้น ๆ (คำนาม/หัวข้อ เช่น ชนิดวัตถุ สถานที่ กิจกรรม สี อารมณ์) เป็นภาษาไทย ไม่ต้องมีเครื่องหมาย # และห้ามซ้ำกัน`

// AnalyzeAI ส่งภาพ (ย่อให้ไม่เกิน 1568px) ไปให้ Gemini วิเคราะห์ ได้คำอธิบายและ tag
func AnalyzeAI(cfg AIConfig, path string) (AIResult, error) {
	if cfg.APIKey == "" {
		return AIResult{}, errors.New("ไม่มี API Key")
	}
	cfg.Model = NormalizeAIModel(cfg.Model)
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultGeminiBaseURL
	}
	mediaType, b64, err := prepareImageForAI(path)
	if err != nil {
		return AIResult{}, err
	}

	body := map[string]any{
		"contents": []any{map[string]any{
			"role": "user",
			"parts": []any{
				map[string]any{"inlineData": map[string]any{"mimeType": mediaType, "data": b64}},
				map[string]any{"text": aiPrompt},
			},
		}},
		"generationConfig": map[string]any{
			// เผื่อ token สำหรับโมเดลที่มี thinking เพื่อไม่ให้คำตอบถูกตัดกลางคัน
			"maxOutputTokens":  2048,
			"responseMimeType": "application/json",
		},
	}
	payload, _ := json.Marshal(body)
	url := strings.TrimRight(cfg.BaseURL, "/") + "/v1beta/models/" + cfg.Model + ":generateContent"

	var data []byte
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
		if err != nil {
			return AIResult{}, err
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-goog-api-key", cfg.APIKey)

		resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		if err != nil {
			return AIResult{}, fmt.Errorf("เชื่อมต่อ API ไม่ได้: %w", err)
		}
		data, _ = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			break
		}
		// free tier มีเพดานคำขอต่อนาที: 429/503 ลองใหม่สั้น ๆ ก่อนยอมแพ้ (แล้ว fallback เป็นวิเคราะห์ในเครื่อง)
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) && attempt < aiMaxAttempts {
			time.Sleep(time.Duration(attempt) * aiRetryDelay)
			continue
		}
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		return AIResult{}, fmt.Errorf("API ตอบกลับ %d: %s", resp.StatusCode, truncate(msg, 200))
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
		return AIResult{}, fmt.Errorf("อ่านคำตอบ API ไม่ได้: %w", err)
	}
	if len(out.Candidates) == 0 {
		if out.PromptFeedback.BlockReason != "" {
			return AIResult{}, fmt.Errorf("Gemini ปฏิเสธรูปนี้ (%s)", out.PromptFeedback.BlockReason)
		}
		return AIResult{}, errors.New("Gemini ไม่ได้ส่งคำตอบกลับมา")
	}
	var text string
	for _, p := range out.Candidates[0].Content.Parts {
		if !p.Thought {
			text += p.Text
		}
	}
	return parseAIJSON(text)
}

// parseAIJSON ดึง JSON ออกจากคำตอบ (กันกรณีโมเดลใส่ ```json ครอบมา)
func parseAIJSON(text string) (AIResult, error) {
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j <= i {
		return AIResult{}, errors.New("คำตอบของ AI ไม่ใช่ JSON")
	}
	var r AIResult
	if err := json.Unmarshal([]byte(text[i:j+1]), &r); err != nil {
		return AIResult{}, fmt.Errorf("แปลง JSON ของ AI ไม่ได้: %w", err)
	}
	r.Description = strings.TrimSpace(r.Description)
	r.Tags = NormalizeTags(r.Tags)
	if r.Description == "" {
		return AIResult{}, errors.New("AI ไม่ได้ส่งคำอธิบายภาพ")
	}
	return r, nil
}

// prepareImageForAI ย่อ/แปลงภาพให้เหมาะกับ API (<= 1568px, JPEG) ถ้าถอดรหัสไม่ได้
// (เช่น webp) และไฟล์ไม่ใหญ่เกิน 4MB จะส่งไฟล์ต้นฉบับ
func prepareImageForAI(path string) (mediaType, b64 string, err error) {
	img, derr := decodeFile(path)
	if derr == nil {
		img = orient(scaleDown(img, 1568), ReadExif(path).Orientation)
		data, err := encodeJPEG(img, 85)
		if err != nil {
			return "", "", err
		}
		return "image/jpeg", base64.StdEncoding.EncodeToString(data), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	mt := http.DetectContentType(raw)
	if (mt != "image/webp" && mt != "image/gif" && mt != "image/png" && mt != "image/jpeg") || len(raw) > 4<<20 {
		return "", "", fmt.Errorf("ส่งรูปชนิดนี้ให้ AI ไม่ได้ (%s): %v", mt, derr)
	}
	return mt, base64.StdEncoding.EncodeToString(raw), nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
