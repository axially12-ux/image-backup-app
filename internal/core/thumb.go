package core

import (
	"image/color"
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func decodeFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// scaleDown ย่อภาพให้ด้านยาวสุดไม่เกิน maxDim ด้วยการเฉลี่ยพิกเซลเป็นบล็อก
// (ไม่ขยายภาพเล็ก) — ใช้ stdlib ล้วน ๆ จึงข้ามแพลตฟอร์มได้
func scaleDown(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return src
	}
	var nw, nh int
	if w >= h {
		nw, nh = maxDim, max(1, h*maxDim/w)
	} else {
		nh, nw = maxDim, max(1, w*maxDim/h)
	}
	if yc, ok := src.(*image.YCbCr); ok {
		return scaleDownYCbCr(yc, nw, nh)
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	rx, ry := float64(w)/float64(nw), float64(h)/float64(nh)
	for y := 0; y < nh; y++ {
		y0 := int(float64(y) * ry)
		y1 := max(y0+1, int(float64(y+1)*ry))
		sy := max(1, (y1-y0)/3)
		for x := 0; x < nw; x++ {
			x0 := int(float64(x) * rx)
			x1 := max(x0+1, int(float64(x+1)*rx))
			sx := max(1, (x1-x0)/3)
			var r, g, bl, a, n uint32
			for yy := y0; yy < y1; yy += sy {
				for xx := x0; xx < x1; xx += sx {
					pr, pg, pb, pa := src.At(b.Min.X+xx, b.Min.Y+yy).RGBA()
					r += pr >> 8
					g += pg >> 8
					bl += pb >> 8
					a += pa >> 8
					n++
				}
			}
			if n == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/n), uint8(g/n), uint8(bl/n), uint8(a/n)
		}
	}
	return dst
}

// orient หมุน/พลิกภาพตามค่า EXIF Orientation (1..8) เพื่อให้ภาพตั้งตรงเหมือนที่ถ่าย
func orient(src image.Image, o int) image.Image {
	if o < 2 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	if o >= 5 {
		nw, nh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func encodeJPEG(img image.Image, q int) ([]byte, error) {
	// JPEG ไม่มี alpha → วางบนพื้นขาวกันพื้นหลังโปร่งใสกลายเป็นดำ
	b := img.Bounds()
	flat := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(flat, flat.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), img, b.Min, draw.Over)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: q}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Thumbnail สร้าง (หรืออ่านจากแคช) รูปย่อของรูปที่ id และคืน bytes + mime
// ถอดรหัสไม่ได้ (เช่น .webp) จะคืนไฟล์ต้นฉบับแทน
func (s *Service) Thumbnail(id uint, maxDim int) ([]byte, string, error) {
	path, err := s.ImagePath(id)
	if err != nil {
		return nil, "", err
	}
	var cache string
	if s.ThumbDir != "" {
		if fi, err := os.Stat(path); err == nil {
			cache = filepath.Join(s.ThumbDir, fmt.Sprintf("%d_%d_%d.jpg", id, maxDim, fi.ModTime().Unix()))
			if data, err := os.ReadFile(cache); err == nil {
				return data, "image/jpeg", nil
			}
		}
	}
	img, err := decodeFile(path)
	if err != nil {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, "", rerr
		}
		return data, http.DetectContentType(data), nil
	}
	img = orient(scaleDown(img, maxDim), ReadExif(path).Orientation)
	data, err := encodeJPEG(img, 80)
	if err != nil {
		return nil, "", err
	}
	if cache != "" {
		_ = os.MkdirAll(s.ThumbDir, 0o755)
		_ = os.WriteFile(cache, data, 0o644)
	}
	return data, "image/jpeg", nil
}

// Middleware เสิร์ฟรูปให้ Frontend ผ่าน URL /media/thumb/<id> และ /media/full/<id>
// (แทนการส่งรูปเป็น base64 ผ่าน binding ซึ่งช้าและกินหน่วยความจำมาก)
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/media/") {
			next.ServeHTTP(w, r)
			return
		}
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/media/"), "/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		id64, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch parts[0] {
		case "thumb":
			data, mime, err := s.Thumbnail(uint(id64), 360)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", mime)
			w.Header().Set("Cache-Control", "private, max-age=3600")
			_, _ = w.Write(data)
		case "full":
			path, err := s.ImagePath(uint(id64))
			if err != nil || !fileExists(path) {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, path)
		default:
			http.NotFound(w, r)
		}
	})
}

// scaleDownYCbCr คือเส้นทางเร็วของ scaleDown สำหรับภาพ JPEG (image.YCbCr)
// อ่านค่า Y/Cb/Cr ตรง ๆ แทน src.At() ซึ่งต้องสร้าง color.Color ทุกพิกเซล (ช้ากว่าหลายเท่า)
// ใช้วิธีสุ่มตัวอย่างเป็นบล็อกแบบเดียวกับเส้นทางทั่วไป
func scaleDownYCbCr(src *image.YCbCr, nw, nh int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	rx, ry := float64(w)/float64(nw), float64(h)/float64(nh)
	for y := 0; y < nh; y++ {
		y0 := int(float64(y) * ry)
		y1 := max(y0+1, int(float64(y+1)*ry))
		sy := max(1, (y1-y0)/3)
		for x := 0; x < nw; x++ {
			x0 := int(float64(x) * rx)
			x1 := max(x0+1, int(float64(x+1)*rx))
			sx := max(1, (x1-x0)/3)
			var r, g, bl, n uint32
			for yy := y0; yy < y1; yy += sy {
				for xx := x0; xx < x1; xx += sx {
					px, py := b.Min.X+xx, b.Min.Y+yy
					cr, cg, cb := color.YCbCrToRGB(src.Y[src.YOffset(px, py)], src.Cb[src.COffset(px, py)], src.Cr[src.COffset(px, py)])
					r += uint32(cr)
					g += uint32(cg)
					bl += uint32(cb)
					n++
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
		}
	}
	return dst
}
