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
const DefaultAIModel = "claude-haiku-5-5"

// AIConfig คือค่าที่ใช้เรียก Anthropic Messages API
type AIConfig struct {
	APIKey  string
	Model   string
	BaseURL string // ค่าว่าง = https://api.anthropic.com (ใช้ทดสอบ)
}

// AIResult คือผลที่ AI ส่งกลับ
type AIResult struct {
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

const aiPrompt = `วิเคราะห์ภาพนี้แล้วตอบเป็น JSON เท่านั้น (ห้ามมีข้อความอื่น):
{"description": "...", "tags": ["...", "..."]}
- description: คำอธิบายภาพเป็นภาษาไทย 1-2 ประโยค บอกว่าในภาพมีอะไร ทำอะไร อยู่ที่ไหน บรรยากาศเป็นอย่างไร
- tags: 5-10 คำสั้น ๆ (คำนาม/หัวข้อ เช่น ชนิดวัตถุ สถานที่ กิจกรรม สี อารมณ์) เป็นภาษาไทย ไม่ต้องมีเครื่องหมาย # และห้ามซ้ำกัน`

// AnalyzeAI ส่งภาพ (ย่อให้ไม่เกิน 1568px) ไปให้ Claude วิเคราะห์ ได้คำอธิบายและ tag
func AnalyzeAI(cfg AIConfig, path string) (AIResult, error) {
	if cfg.APIKey == "" {
		return AIResult{}, errors.New("ไม่มี API Key")
	}
	if cfg.Model == "" {
		cfg.Model = DefaultAIModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com"
	}
	mediaType, b64, err := prepareImageForAI(path)
	if err != nil {
		return AIResult{}, err
	}

	body := map[string]any{
		"model":      cfg.Model,
		"max_tokens": 600,
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": b64}},
				map[string]any{"type": "text", "text": aiPrompt},
			},
		}},
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", strings.TrimRight(cfg.BaseURL, "/")+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return AIResult{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return AIResult{}, fmt.Errorf("เชื่อมต่อ API ไม่ได้: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
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
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return AIResult{}, fmt.Errorf("อ่านคำตอบ API ไม่ได้: %w", err)
	}
	var text string
	for _, c := range out.Content {
		if c.Type == "text" {
			text += c.Text
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
