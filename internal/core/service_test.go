package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeJPEG(t *testing.T, path string, w, h int, c color.Color, topColor color.Color) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if topColor != nil && y < h/3 {
				img.Set(x, y, topColor)
			} else {
				img.Set(x, y, c)
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if strings.HasSuffix(strings.ToLower(path), ".png") {
		png.Encode(f, img)
		return
	}
	jpeg.Encode(f, img, nil)
}

func setup(t *testing.T) (*Service, string, string) {
	t.Helper()
	root := t.TempDir()
	src, dst := filepath.Join(root, "flash"), filepath.Join(root, "backup")
	os.MkdirAll(src, 0o755)
	svc, err := New(newMemStore())
	if err != nil {
		t.Fatal(err)
	}
	return svc, src, dst
}

func TestStateGuard(t *testing.T) {
	svc, _, _ := setup(t)
	if _, err := svc.ListDB(""); err == nil {
		t.Fatal("ListDB ก่อนตั้ง dest ต้องแจ้ง error ไม่ใช่ crash")
	}
	if _, err := svc.Move(nil, true, "move"); err != ErrNoSource {
		t.Fatalf("want ErrNoSource got %v", err)
	}
	if _, err := svc.Check(); err != ErrNoDest {
		t.Fatalf("want ErrNoDest got %v", err)
	}
	if _, err := svc.ListSource(); err != ErrNoSource {
		t.Fatal("ListSource guard")
	}
}

func TestMoveDuplicateCheckDeleteFlow(t *testing.T) {
	svc, src, dst := setup(t)
	// รวมนามสกุลตัวพิมพ์ใหญ่ (ที่กล้อง/มือถือใช้) + ไฟล์ที่ไม่ใช่รูป
	writeJPEG(t, filepath.Join(src, "IMG_001.JPG"), 200, 100, color.RGBA{30, 80, 220, 255}, nil)
	writeJPEG(t, filepath.Join(src, "b.png"), 80, 120, color.RGBA{200, 30, 30, 255}, nil)
	writeJPEG(t, filepath.Join(src, "same-content.jpg"), 200, 100, color.RGBA{30, 80, 220, 255}, nil)
	os.WriteFile(filepath.Join(src, "notes.txt"), []byte("x"), 0o644)

	if n, err := svc.SetSource(src); err != nil || n != 3 {
		t.Fatalf("SetSource n=%d err=%v (ต้องนับ .JPG และไม่นับ .txt)", n, err)
	}
	rep0, err := svc.SetDest(dst)
	if err != nil || !rep0.OK {
		t.Fatalf("SetDest %v %+v", err, rep0)
	}

	var events []ProgressEvent
	svc.OnProgress = func(e ProgressEvent) { events = append(events, e) }

	rep, err := svc.Move([]string{"IMG_001.JPG", "b.png"}, false, "move")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Moved != 2 || rep.Skipped != 0 || rep.Failed != 0 {
		t.Fatalf("report %+v", rep)
	}
	if rep.Elapsed == "" || rep.Analyzed != 2 {
		t.Fatalf("ต้องจับเวลาและวิเคราะห์ภาพ: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(src, "IMG_001.JPG")); err == nil {
		t.Fatal("โหมด move ต้องลบไฟล์ต้นทาง")
	}
	if len(events) < 4 {
		t.Fatalf("ต้องมี progress event ทั้ง move และ analyze: %d", len(events))
	}

	// ชื่อซ้ำ: ใส่ไฟล์ชื่อเดิมกลับเข้า source แล้วย้ายอีก → ต้อง SKIPPED
	writeJPEG(t, filepath.Join(src, "b.png"), 80, 120, color.RGBA{1, 2, 3, 255}, nil)
	rep, _ = svc.Move([]string{"b.png"}, false, "move")
	if rep.Skipped != 1 || rep.Moved != 0 {
		t.Fatalf("ชื่อซ้ำต้องถูกข้าม %+v", rep)
	}
	// เนื้อหาซ้ำ ชื่อต่าง → ต้องถูกข้าม
	rep, _ = svc.Move([]string{"same-content.jpg"}, false, "move")
	if rep.Skipped != 1 || !strings.Contains(rep.Items[0].Message, "ซ้ำ") {
		t.Fatalf("เนื้อหาซ้ำต้องถูกข้าม %+v", rep)
	}
	// ไฟล์ที่ไม่มี / ไม่ใช่รูป / path traversal
	rep, _ = svc.Move([]string{"nope.jpg", "notes.txt"}, false, "move")
	if rep.Failed != 2 {
		t.Fatalf("ต้อง error 2 รายการ %+v", rep)
	}
	if _, err := svc.Move([]string{"../x.jpg"}, false, "move"); err == nil {
		t.Fatal("path traversal ต้องถูกปฏิเสธ")
	}

	// list db / list dest
	db, _ := svc.ListDB("")
	if len(db) != 2 {
		t.Fatalf("DB ต้องมี 2 รายการ %d", len(db))
	}
	if db[0].Description == "" || len(db[0].Tags) < 3 || db[0].Analyzer != "local" {
		t.Fatalf("ต้องมีผลวิเคราะห์: %+v", db[0])
	}
	if other, _ := svc.ListDB(filepath.Join(src, "elsewhere")); len(other) != 0 {
		t.Fatal("ListDB path อื่นต้องว่าง")
	}

	// check: ลบไฟล์นอกโปรแกรม → ต้องเจอ missing
	os.Remove(filepath.Join(dst, "b.png"))
	chk, _ := svc.Check()
	if chk.OK || len(chk.Missing) != 1 || chk.Missing[0] != "b.png" || chk.Verified != 1 {
		t.Fatalf("check %+v", chk)
	}
	// ไฟล์เกินในโฟลเดอร์ปลายทางที่ไม่อยู่ใน DB
	writeJPEG(t, filepath.Join(dst, "extra.jpg"), 10, 10, color.RGBA{9, 9, 9, 255}, nil)
	chk, _ = svc.Check()
	if len(chk.Untracked) != 1 {
		t.Fatalf("untracked %+v", chk)
	}

	// delete: ไฟล์หายแล้วแต่ยังมีประวัติ → ลบประวัติ
	del, err := svc.Delete("dest", []string{"b.png"}, false)
	if err != nil || del.Deleted != 1 || !strings.Contains(del.Items[0].Message, "DB") {
		t.Fatalf("delete %+v %v", del, err)
	}
	del, _ = svc.Delete("dest", []string{"IMG_001.JPG"}, false)
	if del.Deleted != 1 {
		t.Fatalf("delete %+v", del)
	}
	if db, _ = svc.ListDB(""); len(db) != 0 {
		t.Fatal("ลบแล้วประวัติต้องหมด")
	}
	del, _ = svc.Delete("dest", []string{"missing.jpg"}, false)
	if del.Deleted != 0 || del.Items[0].Status != "error" {
		t.Fatalf("ไม่พบไฟล์ต้อง error %+v", del)
	}
}

func TestCopyModeAndAll(t *testing.T) {
	svc, src, dst := setup(t)
	writeJPEG(t, filepath.Join(src, "a.jpg"), 40, 40, color.RGBA{10, 200, 10, 255}, nil)
	writeJPEG(t, filepath.Join(src, "b.JPEG"), 40, 60, color.RGBA{250, 250, 20, 255}, nil)
	svc.SetSource(src)
	svc.SetDest(dst)
	rep, err := svc.Move(nil, true, "copy")
	if err != nil || rep.Moved != 2 || rep.Items[0].Status != "copied" {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, err := os.Stat(filepath.Join(src, "a.jpg")); err != nil {
		t.Fatal("โหมด copy ต้องคงไฟล์ต้นทาง")
	}
	if _, err := svc.Move(nil, true, "move"); err != nil {
		t.Fatal(err)
	}
	svc.Delete("source", nil, true)
	if n, _ := svc.ListSource(); len(n) != 0 {
		t.Fatal("delete source all")
	}
}

func TestSettingsRestore(t *testing.T) {
	store := newMemStore()
	root := t.TempDir()
	src, dst := filepath.Join(root, "s"), filepath.Join(root, "d")
	os.MkdirAll(src, 0o755)
	a, _ := New(store)
	a.SetSource(src)
	a.SetDest(dst)
	b, err := New(store) // เปิดโปรแกรมใหม่
	if err != nil {
		t.Fatal(err)
	}
	st := b.GetState()
	if st.Source != filepath.Clean(src) || st.Dest != filepath.Clean(dst) {
		t.Fatalf("ต้องกู้ session ล่าสุด %+v", st)
	}
	if !strings.Contains(b.RestoredNote(), "กู้ค่า") {
		t.Fatal(b.RestoredNote())
	}
}

func TestSearchAndTagCloud(t *testing.T) {
	svc, src, dst := setup(t)
	writeJPEG(t, filepath.Join(src, "sky.jpg"), 300, 150, color.RGBA{40, 140, 40, 255}, color.RGBA{120, 180, 250, 255})
	writeJPEG(t, filepath.Join(src, "red.png"), 100, 160, color.RGBA{210, 30, 30, 255}, nil)
	svc.SetSource(src)
	svc.SetDest(dst)
	if _, err := svc.Move(nil, true, "move"); err != nil {
		t.Fatal(err)
	}
	cloud, _ := svc.TagCloud()
	m := map[string]int{}
	for _, c := range cloud {
		m[c.Name] = c.Count
	}
	if m["backup"] != 2 || m["แนวนอน"] != 1 || m["แนวตั้ง"] != 1 {
		t.Fatalf("tag cloud %v", m)
	}
	r, _ := svc.Search("", "แนวตั้ง")
	if len(r) != 1 || r[0].Filename != "red.png" {
		t.Fatalf("search tag %v", r)
	}
	r, _ = svc.Search("แดง", "")
	if len(r) != 1 {
		t.Fatalf("search desc %v", r)
	}
	r, _ = svc.Search("ภาพ แนวนอน", "")
	if len(r) != 1 || r[0].Filename != "sky.jpg" {
		t.Fatalf("multi keyword %v", r)
	}
	r, _ = svc.Search("", "#แนวนอน")
	if len(r) != 1 {
		t.Fatal("tag ต้อง normalize ตัด #")
	}
}

func TestLocalAnalyzerContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "scene.jpg")
	writeJPEG(t, p, 320, 240, color.RGBA{30, 150, 40, 255}, color.RGBA{110, 170, 250, 255})
	r, err := AnalyzeLocal(p)
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, tg := range r.Tags {
		has[tg] = true
	}
	for _, want := range []string{"แนวนอน", "ท้องฟ้า", "ธรรมชาติ", "โทนเขียว", "jpg"} {
		if !has[want] {
			t.Errorf("ขาด tag %q ใน %v / %s", want, r.Tags, r.Description)
		}
	}
	if !strings.Contains(r.Description, "320×240") {
		t.Fatal(r.Description)
	}
	g := filepath.Join(dir, "gray.png")
	writeJPEG(t, g, 50, 50, color.RGBA{128, 128, 128, 255}, nil)
	r, _ = AnalyzeLocal(g)
	found := false
	for _, tg := range r.Tags {
		if tg == "ขาวดำ" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ภาพเทาต้องได้ tag ขาวดำ %v", r.Tags)
	}
}

func TestExifParse(t *testing.T) {
	// สร้าง JPEG ที่มี APP1 EXIF (little-endian): Make, Model, Orientation=6, DateTimeOriginal
	tiff := buildTIFF()
	seg := append([]byte("Exif\x00\x00"), tiff...)
	b := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte((len(seg) + 2) >> 8), byte((len(seg) + 2) & 0xFF)}
	b = append(b, seg...)
	b = append(b, 0xFF, 0xD9)
	info := parseExif(b)
	if info.Make != "Apple" || info.Model != "iPhone 13" || info.Orientation != 6 {
		t.Fatalf("%+v", info)
	}
	if !info.HasTaken || info.Taken.Hour() != 17 || info.Taken.Year() != 2026 {
		t.Fatalf("%+v", info)
	}
	if got := timeOfDay(info.Taken.Hour()); got != "เย็น" {
		t.Fatal(got)
	}
}

func buildTIFF() []byte {
	le := func(v uint32, n int) []byte {
		out := make([]byte, n)
		for i := 0; i < n; i++ {
			out[i] = byte(v >> (8 * i))
		}
		return out
	}
	entry := func(tag, typ uint16, count uint32, val uint32) []byte {
		var e []byte
		e = append(e, le(uint32(tag), 2)...)
		e = append(e, le(uint32(typ), 2)...)
		e = append(e, le(count, 4)...)
		e = append(e, le(val, 4)...)
		return e
	}
	// layout: header(8) | IFD0 (2+4*12+4=54) | strings | exifIFD
	make := "Apple\x00"
	model := "iPhone 13\x00"
	date := "2026:09:12 17:05:09\x00"
	ifd0Off := uint32(8)
	strOff := ifd0Off + 54
	makeOff := strOff
	modelOff := makeOff + uint32(len(make))
	exifOff := modelOff + uint32(len(model))
	dateOff := exifOff + 2 + 12 + 4
	var t []byte
	t = append(t, 'I', 'I', 42, 0)
	t = append(t, le(ifd0Off, 4)...)
	t = append(t, le(4, 2)...)
	t = append(t, entry(0x010F, 2, uint32(len(make)), makeOff)...)
	t = append(t, entry(0x0110, 2, uint32(len(model)), modelOff)...)
	t = append(t, entry(0x0112, 3, 1, 6)...)
	t = append(t, entry(0x8769, 4, 1, exifOff)...)
	t = append(t, le(0, 4)...)
	t = append(t, []byte(make)...)
	t = append(t, []byte(model)...)
	t = append(t, le(1, 2)...)
	t = append(t, entry(0x9003, 2, uint32(len(date)), dateOff)...)
	t = append(t, le(0, 4)...)
	t = append(t, []byte(date)...)
	return t
}

func TestOrientRotates(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	got := orient(src, 6)
	if got.Bounds().Dx() != 2 || got.Bounds().Dy() != 4 {
		t.Fatalf("orientation 6 ต้องสลับกว้าง/สูง %v", got.Bounds())
	}
	big := image.NewRGBA(image.Rect(0, 0, 4000, 3000))
	s := scaleDown(big, 360)
	if s.Bounds().Dx() != 360 || s.Bounds().Dy() != 270 {
		t.Fatalf("scale %v", s.Bounds())
	}
}

func TestAIAnalyzerWithFakeServer(t *testing.T) {
	var gotKey, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-goog-api-key")
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		// Gemini อาจห่อ JSON ด้วย ```json ก็ได้ — parseAIJSON ต้องรับได้
		answer := "```json\n" + `{"results":[{"i":0,"description":"แมวส้มนอนบนโซฟา","categories":["สัตว์","สัตว์","แมวส้ม","ในอาคาร"]}]}` + "\n```"
		resp := map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{
				map[string]any{"text": "(thinking...)", "thought": true},
				map[string]any{"text": answer},
			}},
			"finishReason": "STOP",
		}}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	svc, src, dst := setup(t)
	writeJPEG(t, filepath.Join(src, "cat.jpg"), 64, 64, color.RGBA{230, 140, 40, 255}, nil)
	svc.SetSource(src)
	svc.SetDest(dst)
	svc.AIBaseURL = srv.URL
	if err := svc.SaveAISettings(AISettingsInput{Enabled: true}); err == nil {
		t.Fatal("เปิด AI โดยไม่มี key ต้อง error")
	}
	if err := svc.SaveAISettings(AISettingsInput{Enabled: true, Key: "AIza-test", Model: "m-1"}); err != nil {
		t.Fatal(err)
	}
	rep, err := svc.Move(nil, true, "move")
	if err != nil || rep.AIUsed != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if gotKey != "AIza-test" || gotPath != "/v1beta/models/m-1:generateContent" {
		t.Fatal(gotKey, gotPath)
	}
	contents := gotBody["contents"].([]any)
	parts := contents[0].(map[string]any)["parts"].([]any)
	hasImg := false
	for _, p := range parts {
		if _, ok := p.(map[string]any)["inlineData"]; ok {
			hasImg = true
		}
	}
	if !hasImg {
		t.Fatal("ต้องส่งรูปให้ AI")
	}
	db, _ := svc.ListDB("")
	if db[0].Description != "แมวส้มนอนบนโซฟา" || db[0].Analyzer != "ai" {
		t.Fatalf("%+v", db[0])
	}
	names := map[string]int{}
	for _, tg := range db[0].Tags {
		names[tg.Name]++
	}
	// แท็กต้องเป็นหมวดหมู่ในรายการเท่านั้น (คำนอกรายการอย่าง "แมวส้ม" ถูกทิ้ง) + backup
	if names["สัตว์"] != 1 || names["ในอาคาร"] != 1 || names["backup"] != 1 || len(names) != 3 {
		t.Fatalf("tags %v", names)
	}

	// AI ล้ม (400 key ผิด) → fallback local และมี note
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"message":"API key not valid"}}`)
	}))
	defer bad.Close()
	svc.AIBaseURL = bad.URL
	rr, err := svc.Reanalyze(nil, true)
	if err != nil || rr.Done != 1 || rr.AIUsed != 0 || !strings.Contains(rr.Note, "400") {
		t.Fatalf("%+v %v", rr, err)
	}
	db, _ = svc.ListDB("")
	if db[0].Analyzer != "local" {
		t.Fatal("ต้อง fallback เป็น local")
	}
}

func TestAIRetriesOnRateLimit(t *testing.T) {
	oldDelay := aiRetryDelay
	aiRetryDelay = time.Millisecond
	defer func() { aiRetryDelay = oldDelay }()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"quota"}}`)
			return
		}
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"{\"results\":[{\"i\":0,\"description\":\"ทะเล\",\"categories\":[\"ทะเล\"]}]}"}]}}]}`)
	}))
	defer srv.Close()

	_, src, _ := setup(t)
	img := filepath.Join(src, "sea.jpg")
	writeJPEG(t, img, 64, 64, color.RGBA{20, 80, 200, 255}, nil)
	res, err := AnalyzeAI(AIConfig{APIKey: "k", BaseURL: srv.URL}, img)
	if err != nil || calls != 3 || res.Description != "ทะเล" {
		t.Fatalf("calls=%d res=%+v err=%v", calls, res, err)
	}
}

func TestNormalizeAIModel(t *testing.T) {
	cases := map[string]string{
		"":                        DefaultAIModel,
		"claude-haiku-5-5":        DefaultAIModel, // ชื่อโมเดลเก่าจากเวอร์ชันก่อน
		"models/gemini-x":         "gemini-x",
		" gemini-3.5-flash-lite ": "gemini-3.5-flash-lite",
	}
	for in, want := range cases {
		if got := NormalizeAIModel(in); got != want {
			t.Errorf("NormalizeAIModel(%q)=%q want %q", in, got, want)
		}
	}
}

func TestMediaMiddleware(t *testing.T) {
	svc, src, dst := setup(t)
	svc.ThumbDir = filepath.Join(t.TempDir(), "thumbs")
	writeJPEG(t, filepath.Join(src, "a.jpg"), 800, 600, color.RGBA{10, 10, 200, 255}, nil)
	svc.SetSource(src)
	svc.SetDest(dst)
	svc.Move(nil, true, "move")
	db, _ := svc.ListDB("")
	h := svc.Middleware(http.NotFoundHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("/media/thumb/%d", db[0].ID), nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatal(rec.Code, rec.Header())
	}
	cfg, err := jpeg.DecodeConfig(rec.Body)
	if err != nil || cfg.Width != 360 {
		t.Fatalf("thumb %v %v", cfg, err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("/media/full/%d", db[0].ID), nil))
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/media/full/999", nil))
	if rec.Code != 404 {
		t.Fatal("ไม่พบต้อง 404")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/index.html", nil))
	if rec.Code != 404 { // ส่งต่อให้ next
		t.Fatal("path อื่นต้องส่งต่อ")
	}
}

func TestFormatElapsed(t *testing.T) {
	if got := FormatElapsed(532_100_000); got != "532.10 ms" {
		t.Fatal(got)
	}
	if got := FormatElapsed(2_410_000_000); got != "2.41 s" {
		t.Fatal(got)
	}
}


func TestNormalizeCategories(t *testing.T) {
	got := NormalizeCategories([]string{"ทะเล", "ทะเล", "แมวส้ม", "ผู้หญิง", "ภูเขา", "ธรรมชาติ", "อาหาร"})
	if strings.Join(got, ",") != "ทะเล,ผู้หญิง,ภูเขา,ธรรมชาติ,อาหาร" { // ไม่ซ้ำ, ไม่เกิน 5, ตัดคำนอกรายการ
		t.Fatal(got)
	}
	seen := map[string]bool{}
	for _, c := range AICategories { // รายการหมวดต้องไม่ซ้ำกัน (ซ้ำจะทำให้ enum/ผลคลาดเคลื่อน)
		if seen[NormalizeTag(c)] {
			t.Fatalf("หมวดซ้ำ: %s", c)
		}
		seen[NormalizeTag(c)] = true
	}
	if got := NormalizeCategories([]string{"อะไรก็ไม่รู้"}); len(got) != 1 || got[0] != "อื่นๆ" {
		t.Fatal(got)
	}
}

// fakeBatchServer ตอบทุกรูปในคำขอเป็นหมวด "ทะเล" และนับจำนวน request / รูปต่อ request
func fakeBatchServer(t *testing.T, calls *int32, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" { // นับเฉพาะ generateContent (ไม่นับ ListModels)
			atomic.AddInt32(calls, 1)
		}
		if status != 200 {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"Quota exceeded for metric ... PerDay"}}`)
			return
		}
		var body struct {
			Contents []struct {
				Parts []map[string]any `json:"parts"`
			} `json:"contents"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		n := 0
		for _, p := range body.Contents[0].Parts {
			if _, ok := p["inlineData"]; ok {
				n++
			}
		}
		rows := []string{}
		for i := 0; i < n; i++ {
			rows = append(rows, fmt.Sprintf(`{"i":%d,"description":"ภาพทะเล %d","categories":["ทะเล","ท้องฟ้า"]}`, i, i))
		}
		text, _ := json.Marshal(`{"results":[` + strings.Join(rows, ",") + `]}`)
		fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%s}]}}]}`, text)
	}))
}

func TestAIBatchesManyImagesPerRequest(t *testing.T) {
	var calls int32
	srv := fakeBatchServer(t, &calls, 200)
	defer srv.Close()

	svc, src, dst := setup(t)
	for i := 0; i < 20; i++ {
		writeJPEG(t, filepath.Join(src, fmt.Sprintf("p%02d.jpg", i)), 48+i, 48, color.RGBA{20, 80, 200, 255}, nil)
	}
	svc.SetSource(src)
	svc.SetDest(dst)
	svc.AIBaseURL = srv.URL
	svc.AIBatchSize = 8
	if err := svc.SaveAISettings(AISettingsInput{Enabled: true, Key: "k"}); err != nil {
		t.Fatal(err)
	}
	rep, err := svc.Move(nil, true, "move")
	if err != nil || rep.Analyzed != 20 || rep.AIUsed != 20 {
		t.Fatalf("%+v %v", rep, err)
	}
	if calls != 3 { // 20 รูป ÷ 8 = 3 request (แทนที่จะเป็น 20)
		t.Fatalf("ควรใช้ 3 request ได้ %d", calls)
	}
	db, _ := svc.ListDB("")
	for _, im := range db {
		if im.Width == 0 || len(im.Tags) != 3 { // backup + ทะเล + ท้องฟ้า
			t.Fatalf("%+v", im)
		}
	}
}

func TestAIQuotaStopsEarlyAndFallsBackLocal(t *testing.T) {
	var calls int32
	srv := fakeBatchServer(t, &calls, 429)
	defer srv.Close()
	old := aiRetryDelay
	aiRetryDelay = time.Millisecond
	defer func() { aiRetryDelay = old }()

	svc, src, dst := setup(t)
	for i := 0; i < 12; i++ {
		writeJPEG(t, filepath.Join(src, fmt.Sprintf("p%02d.jpg", i)), 48+i, 48, color.RGBA{20, 80, 200, 255}, nil)
	}
	svc.SetSource(src)
	svc.SetDest(dst)
	svc.AIBaseURL = srv.URL
	svc.AIBatchSize = 4
	svc.AIConcurrency = 1
	svc.SaveAISettings(AISettingsInput{Enabled: true, Key: "k"})
	rep, err := svc.Move(nil, true, "move")
	if err != nil || rep.Analyzed != 12 || rep.AIUsed != 0 || !strings.Contains(rep.Note, "โควตา") {
		t.Fatalf("%+v %v", rep, err)
	}
	if calls != 1 { // โควตารายวันหมด → ยิงครั้งเดียวแล้วหยุด ไม่ลองซ้ำ/ไม่ยิงชุดที่เหลือ
		t.Fatalf("ควรยิงแค่ 1 ครั้ง ได้ %d", calls)
	}
}

// โมเดลที่ไม่รับ thinkingConfig (400) → ต้องลองใหม่โดยไม่ส่ง และไม่ทำให้การวิเคราะห์ล้มเหลว
func TestAIThinkingConfigFallback(t *testing.T) {
	aiThinkingOff.Store(true)
	defer aiThinkingOff.Store(true)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, has := body["generationConfig"].(map[string]any)["thinkingConfig"]; has {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"Unknown name \"thinkingConfig\": Cannot find field."}}`)
			return
		}
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"{\"results\":[{\"i\":0,\"description\":\"ทะเล\",\"categories\":[\"ทะเล\"]}]}"}]}}]}`)
	}))
	defer srv.Close()
	_, src, _ := setup(t)
	img := filepath.Join(src, "sea.jpg")
	writeJPEG(t, img, 64, 64, color.RGBA{20, 80, 200, 255}, nil)
	res, err := AnalyzeAI(AIConfig{APIKey: "k", BaseURL: srv.URL}, img)
	if err != nil || calls != 2 || res.Description != "ทะเล" || aiThinkingOff.Load() {
		t.Fatalf("calls=%d res=%+v err=%v", calls, res, err)
	}
}

// 503 (เซิร์ฟเวอร์ยุ่ง) ที่หายเอง → ต้องลองใหม่แล้วสำเร็จ ไม่ fallback; และ Gate ต้องลดแล้วฟื้นกลับ
func TestAIBusy503RecoversAndGateAdapts429(t *testing.T) {
	old := aiRetryDelay
	aiRetryDelay = time.Millisecond
	defer func() { aiRetryDelay = old }()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"message":"This model is currently experiencing high demand."}}`)
			return
		}
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"{\"results\":[{\"i\":0,\"description\":\"ทะเล\",\"categories\":[\"ทะเล\"]}]}"}]}}]}`)
	}))
	defer srv.Close()
	_, src, _ := setup(t)
	img := filepath.Join(src, "sea.jpg")
	writeJPEG(t, img, 64, 64, color.RGBA{20, 80, 200, 255}, nil)
	g := NewAIGate(6)
	res, err := AnalyzeAI(AIConfig{APIKey: "k", BaseURL: srv.URL, Gate: g}, img)
	if err != nil || calls != 3 || res.Description != "ทะเล" {
		t.Fatalf("calls=%d res=%+v err=%v", calls, res, err)
	}
	if g.Cap() != 6 { // 503 ไม่ลดจำนวนที่ยิงพร้อมกัน (ลดเฉพาะ 429)
		t.Fatalf("cap=%d", g.Cap())
	}
	g.Acquire()
	g.Release(true, false) // 429 → ลดครึ่ง
	if g.Cap() != 3 {
		t.Fatalf("cap=%d", g.Cap())
	}

	// 503 ตลอด → ErrAIBusy (ไม่ใช่ ErrAIQuota)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":{"message":"high demand"}}`)
	}))
	defer bad.Close()
	_, err = AnalyzeAI(AIConfig{APIKey: "k", BaseURL: bad.URL}, img)
	if !errors.Is(err, ErrAIBusy) || errors.Is(err, ErrAIQuota) {
		t.Fatalf("err=%v", err)
	}
}

// เกินงบเวลา → ไม่ยิง AI เลย ที่เหลือใช้แบบในเครื่อง และงานเสร็จครบ
func TestAIBudgetExhaustedFallsBackLocal(t *testing.T) {
	var calls int32
	srv := fakeBatchServer(t, &calls, 200)
	defer srv.Close()
	svc, src, dst := setup(t)
	for i := 0; i < 6; i++ {
		writeJPEG(t, filepath.Join(src, fmt.Sprintf("p%02d.jpg", i)), 48+i, 48, color.RGBA{20, 80, 200, 255}, nil)
	}
	svc.SetSource(src)
	svc.SetDest(dst)
	svc.AIBaseURL = srv.URL
	svc.AIBudget = time.Nanosecond
	svc.SaveAISettings(AISettingsInput{Enabled: true, Key: "k"})
	rep, err := svc.Move(nil, true, "move")
	if err != nil || rep.Analyzed != 6 || rep.AIUsed != 0 || calls != 0 || !strings.Contains(rep.Note, "เวลา") {
		t.Fatalf("%+v calls=%d %v", rep, calls, err)
	}
}

// 503 ตลอดและงบเวลาสั้น → ไม่รอ backoff เกินงบ (คืน ErrAIBudget ทันที)
func TestAIBusyRespectsDeadline(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":{"message":"high demand"}}`)
	}))
	defer bad.Close()
	_, src, _ := setup(t)
	img := filepath.Join(src, "a.jpg")
	writeJPEG(t, img, 64, 64, color.RGBA{20, 80, 200, 255}, nil)
	start := time.Now()
	_, err := AnalyzeAI(AIConfig{APIKey: "k", BaseURL: bad.URL, Deadline: time.Now().Add(500 * time.Millisecond)}, img)
	if !errors.Is(err, ErrAIBudget) || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v took=%v", err, time.Since(start))
	}
}

// โมเดลหลักติด 503 ตลอด → สลับไปโมเดลสำรองจาก ListModels อัตโนมัติ แล้วได้ผล AI ครบ
func TestAIFallsBackToAnotherModelOn503(t *testing.T) {
	old := aiRetryDelay
	aiRetryDelay = time.Millisecond
	defer func() { aiRetryDelay = old }()
	var primaryCalls, backupCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1beta/models":
			fmt.Fprint(w, `{"models":[
			 {"name":"models/gemini-x-flash","supportedGenerationMethods":["generateContent"]},
			 {"name":"models/gemini-x-flash-lite-backup","supportedGenerationMethods":["generateContent"]},
			 {"name":"models/gemini-x-flash-image","supportedGenerationMethods":["generateContent"]},
			 {"name":"models/text-embedding","supportedGenerationMethods":["embedContent"]}]}`)
		case strings.Contains(r.URL.Path, "primary-lite"):
			atomic.AddInt32(&primaryCalls, 1)
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"message":"high demand"}}`)
		default:
			atomic.AddInt32(&backupCalls, 1)
			var body struct {
				Contents []struct {
					Parts []map[string]any `json:"parts"`
				} `json:"contents"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			n := 0
			for _, p := range body.Contents[0].Parts {
				if _, ok := p["inlineData"]; ok {
					n++
				}
			}
			rows := []string{}
			for i := 0; i < n; i++ {
				rows = append(rows, fmt.Sprintf(`{"i":%d,"description":"ทะเล","categories":["ทะเล"]}`, i))
			}
			text, _ := json.Marshal(`{"results":[` + strings.Join(rows, ",") + `]}`)
			fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%s}]}}]}`, text)
		}
	}))
	defer srv.Close()

	svc, src, dst := setup(t)
	for i := 0; i < 6; i++ {
		writeJPEG(t, filepath.Join(src, fmt.Sprintf("p%02d.jpg", i)), 48+i, 48, color.RGBA{20, 80, 200, 255}, nil)
	}
	svc.SetSource(src)
	svc.SetDest(dst)
	svc.AIBaseURL = srv.URL
	svc.AIBatchSize = 2
	svc.SaveAISettings(AISettingsInput{Enabled: true, Key: "k", Model: "primary-lite"})
	rep, err := svc.Move(nil, true, "move")
	if err != nil || rep.Analyzed != 6 || rep.AIUsed != 6 {
		t.Fatalf("%+v %v", rep, err)
	}
	if !strings.Contains(rep.Detail, "gemini-x-flash-lite-backup") || strings.Contains(rep.Detail, "image") {
		t.Fatalf("detail=%q", rep.Detail)
	}
	if primaryCalls == 0 || backupCalls == 0 {
		t.Fatal(primaryCalls, backupCalls)
	}
}

// ReanalyzePending ทำเฉพาะรูปที่ยังไม่ใช่ analyzer=ai และไม่ยิงซ้ำกับรูปที่ได้ผล AI แล้ว
func TestReanalyzePendingOnlyNonAI(t *testing.T) {
	var calls int32
	srv := fakeBatchServer(t, &calls, 200)
	defer srv.Close()
	svc, src, dst := setup(t)
	for i := 0; i < 5; i++ {
		writeJPEG(t, filepath.Join(src, fmt.Sprintf("p%02d.jpg", i)), 48+i, 48, color.RGBA{20, 80, 200, 255}, nil)
	}
	svc.SetSource(src)
	svc.SetDest(dst)
	svc.AIBaseURL = srv.URL
	if _, err := svc.Move(nil, true, "move"); err != nil { // AI ปิดอยู่ → ทุกรูปเป็น local
		t.Fatal(err)
	}
	svc.SaveAISettings(AISettingsInput{Enabled: true, Key: "k"})
	rep, err := svc.ReanalyzePending()
	if err != nil || rep.Total != 5 || rep.AIUsed != 5 {
		t.Fatalf("%+v %v", rep, err)
	}
	before := calls
	if _, err := svc.ReanalyzePending(); err == nil || calls != before {
		t.Fatalf("รอบสอง ต้องแจ้งว่าไม่มีอะไรต้องทำ และไม่ยิง API (err=%v calls=%d→%d)", err, before, calls)
	}
}
