package core

import (
	"fmt"
	"image"
	_ "image/gif"  // ลงทะเบียน decoder
	_ "image/jpeg" // ลงทะเบียน decoder
	_ "image/png"  // ลงทะเบียน decoder
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LocalResult คือผลวิเคราะห์ในเครื่อง (ออฟไลน์ ไม่ต้องใช้อินเทอร์เน็ต)
type LocalResult struct {
	Analysis
	// MetaTags คือ tag ที่ได้จาก EXIF/ขนาดไฟล์ (ใช้ได้แม้ถอดรหัสภาพไม่ได้)
	// และถูกนำไปเสริมให้ผลของ Vision AI ด้วย
	MetaTags []string
}

var thaiMonths = []string{"", "มกราคม", "กุมภาพันธ์", "มีนาคม", "เมษายน", "พฤษภาคม", "มิถุนายน",
	"กรกฎาคม", "สิงหาคม", "กันยายน", "ตุลาคม", "พฤศจิกายน", "ธันวาคม"}

// AnalyzeLocal วิเคราะห์ภาพโดยไม่ต้องเรียกบริการภายนอก:
//   - อ่านขนาดภาพ → แนวภาพ (นอน/ตั้ง/จัตุรัส), ความละเอียด
//   - อ่าน EXIF → กล้อง/รุ่น, วันที่-เวลาถ่าย (ช่วงเช้า/บ่าย/เย็น/กลางคืน), GPS
//   - วิเคราะห์พิกเซล → สีเด่น, ความสว่าง, ความสดของสี, ขาวดำ, สัดส่วนท้องฟ้า/พืชพรรณ
//
// หมายเหตุ: การรู้ว่า "ในภาพมีอะไร" (คน/แมว/อาหาร) ต้องใช้ Vision AI (เปิดในหน้าตั้งค่า)
func AnalyzeLocal(path string) (LocalResult, error) {
	var res LocalResult
	res.Analyzer = "local"
	name := filepath.Base(path)
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if ext == "jpeg" {
		ext = "jpg"
	}

	ex := ReadExif(path)
	var metaTags []string
	var metaParts []string
	if ex.Make != "" || ex.Model != "" {
		cam := strings.TrimSpace(ex.Make)
		model := strings.TrimSpace(ex.Model)
		if cam != "" && !strings.HasPrefix(strings.ToLower(model), strings.ToLower(cam)) {
			cam = cam + " " + model
		} else if model != "" {
			cam = model
		}
		if brand := strings.Fields(ex.Make + " " + ex.Model); len(brand) > 0 {
			metaTags = append(metaTags, brand[0])
		}
		metaParts = append(metaParts, "ถ่ายด้วย "+cam)
	}
	if ex.HasTaken {
		t := ex.Taken
		metaTags = append(metaTags, fmt.Sprintf("%d", t.Year()), thaiMonths[t.Month()], timeOfDay(t.Hour()))
		metaParts = append(metaParts, fmt.Sprintf("เมื่อ %d %s %d ช่วง%s", t.Day(), thaiMonths[t.Month()], t.Year()+543, timeOfDay(t.Hour())))
	}
	if ex.HasGPS {
		metaTags = append(metaTags, "มีพิกัด gps")
	}
	metaTags = append(metaTags, ext)
	res.MetaTags = NormalizeTags(metaTags)

	f, err := os.Open(path)
	if err != nil {
		return res, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return res, fmt.Errorf("ถอดรหัสภาพไม่ได้: %w", err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	res.Width, res.Height = w, h
	// ภาพ JPEG จากมือถือมัก "หมุน" ด้วย EXIF orientation 5-8 → สลับกว้าง/สูงสำหรับการบอกแนวภาพ
	dw, dh := w, h
	if ex.Orientation >= 5 && ex.Orientation <= 8 {
		dw, dh = h, w
	}

	var tags []string
	var orient string
	switch {
	case math.Abs(float64(dw-dh)) <= float64(dw)*0.05:
		orient = "จัตุรัส"
	case dw > dh:
		orient = "แนวนอน"
	default:
		orient = "แนวตั้ง"
	}
	tags = append(tags, orient)
	mp := float64(w*h) / 1e6
	switch {
	case mp >= 8:
		tags = append(tags, "ความละเอียดสูง")
	case mp < 0.5:
		tags = append(tags, "ภาพเล็ก")
	}

	st := analyzePixels(img)
	tags = append(tags, st.tags...)

	lower := strings.ToLower(name)
	isShot := strings.Contains(lower, "screenshot") || strings.Contains(lower, "screen shot") || strings.Contains(name, "สกรีน")
	if isShot {
		tags = append(tags, "สกรีนช็อต")
	}

	// ---- ประกอบคำอธิบายภาษาไทย ----
	parts := []string{fmt.Sprintf("ภาพ%s ขนาด %d×%d พิกเซล", orient, w, h)}
	if isShot {
		parts[0] = fmt.Sprintf("ภาพสกรีนช็อต%s ขนาด %d×%d พิกเซล", orient, w, h)
	}
	if st.mono {
		parts = append(parts, "เป็นภาพขาวดำ/โทนเทา")
	} else if len(st.colorNames) > 0 {
		parts = append(parts, "โทนสี"+strings.Join(st.colorNames, "และ"))
	}
	parts = append(parts, st.brightWord)
	if st.sky {
		parts = append(parts, "มีบริเวณท้องฟ้าด้านบนภาพ")
	}
	if st.greenery {
		parts = append(parts, "มีพืชพรรณ/สีเขียวเป็นส่วนมาก")
	}
	parts = append(parts, metaParts...)
	res.Description = strings.Join(parts, " ")
	res.Tags = NormalizeTags(append(tags, res.MetaTags...))
	return res, nil
}

func timeOfDay(hour int) string {
	switch {
	case hour >= 5 && hour < 11:
		return "เช้า"
	case hour >= 11 && hour < 15:
		return "กลางวัน"
	case hour >= 15 && hour < 18:
		return "เย็น"
	case hour >= 18 && hour < 20:
		return "ค่ำ"
	default:
		return "กลางคืน"
	}
}

type pixelStats struct {
	colorNames []string
	tags       []string
	brightWord string
	mono       bool
	sky        bool
	greenery   bool
}

// analyzePixels สุ่มตัวอย่างพิกเซลเป็นตาราง ~48x48 เพื่อความเร็ว แล้วสรุปสี/แสง
func analyzePixels(img image.Image) pixelStats {
	b := img.Bounds()
	const grid = 48
	stepX := max(1, b.Dx()/grid)
	stepY := max(1, b.Dy()/grid)

	counts := map[string]int{}
	var n int
	var sumV, sumS float64
	var topN, topSky, botN, botGreen int
	var greenAll, bluish int

	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bl, a := img.At(x, y).RGBA()
			if a == 0 {
				continue
			}
			hh, ss, vv := rgbToHSV(uint8(r>>8), uint8(g>>8), uint8(bl>>8))
			name := colorName(hh, ss, vv)
			counts[name]++
			n++
			sumV += vv
			sumS += ss
			if name == "เขียว" {
				greenAll++
			}
			if name == "ฟ้า" || name == "น้ำเงิน" {
				bluish++
			}
			rel := float64(y-b.Min.Y) / float64(b.Dy())
			if rel < 0.33 {
				topN++
				if (name == "ฟ้า" || name == "น้ำเงิน" || (name == "ขาว" && vv > 0.85)) && vv > 0.55 {
					topSky++
				}
			}
			if rel > 0.55 {
				botN++
				if name == "เขียว" {
					botGreen++
				}
			}
		}
	}
	var st pixelStats
	if n == 0 {
		st.brightWord = "ไม่ทราบความสว่าง"
		return st
	}
	meanV, meanS := sumV/float64(n), sumS/float64(n)

	switch {
	case meanV < 0.25:
		st.brightWord, st.tags = "ค่อนข้างมืด", append(st.tags, "ภาพมืด")
	case meanV > 0.72:
		st.brightWord, st.tags = "สว่างสดใส", append(st.tags, "ภาพสว่าง")
	default:
		st.brightWord = "แสงปานกลาง"
	}
	if meanS < 0.08 {
		st.mono = true
		st.tags = append(st.tags, "ขาวดำ")
	} else if meanS > 0.5 {
		st.tags = append(st.tags, "สีสด")
	}

	type kv struct {
		k string
		v int
	}
	var arr []kv
	for k, v := range counts {
		arr = append(arr, kv{k, v})
	}
	sort.Slice(arr, func(i, j int) bool {
		if arr[i].v != arr[j].v {
			return arr[i].v > arr[j].v
		}
		return arr[i].k < arr[j].k
	})
	for i, e := range arr {
		if i >= 2 || float64(e.v)/float64(n) < 0.18 {
			break
		}
		st.colorNames = append(st.colorNames, e.k)
		st.tags = append(st.tags, "โทน"+e.k)
	}
	if topN > 0 && float64(topSky)/float64(topN) > 0.6 && float64(bluish)/float64(n) < 0.9 {
		st.sky = true
		st.tags = append(st.tags, "ท้องฟ้า")
	}
	if float64(greenAll)/float64(n) > 0.35 || (botN > 0 && float64(botGreen)/float64(botN) > 0.5) {
		st.greenery = true
		st.tags = append(st.tags, "ธรรมชาติ")
	}
	return st
}

func rgbToHSV(r, g, b uint8) (h, s, v float64) {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	mx := math.Max(rf, math.Max(gf, bf))
	mn := math.Min(rf, math.Min(gf, bf))
	d := mx - mn
	v = mx
	if mx > 0 {
		s = d / mx
	}
	if d == 0 {
		return 0, s, v
	}
	switch mx {
	case rf:
		h = math.Mod((gf-bf)/d, 6)
	case gf:
		h = (bf-rf)/d + 2
	default:
		h = (rf-gf)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return
}

func colorName(h, s, v float64) string {
	switch {
	case v < 0.2:
		return "ดำ"
	case s < 0.14:
		if v > 0.82 {
			return "ขาว"
		}
		return "เทา"
	case h < 15 || h >= 345:
		if v < 0.5 {
			return "น้ำตาล"
		}
		return "แดง"
	case h < 40:
		if v < 0.65 {
			return "น้ำตาล"
		}
		return "ส้ม"
	case h < 70:
		return "เหลือง"
	case h < 165:
		return "เขียว"
	case h < 215:
		return "ฟ้า"
	case h < 255:
		return "น้ำเงิน"
	case h < 290:
		return "ม่วง"
	default:
		return "ชมพู"
	}
}

func baseNoExt(path string) string {
	b := filepath.Base(path)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

func joinPath(dir, name string) string { return filepath.Join(dir, name) }
