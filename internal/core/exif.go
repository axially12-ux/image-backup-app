package core

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"time"
)

// ExifInfo คือข้อมูล EXIF ที่ใช้ (อ่านเองด้วย stdlib ไม่พึ่งไลบรารีภายนอก)
type ExifInfo struct {
	Make        string
	Model       string
	Orientation int // 1..8 (0 = ไม่มี)
	Taken       time.Time
	HasTaken    bool
	HasGPS      bool
}

// ReadExif อ่าน EXIF จากไฟล์ JPEG; ไม่ใช่ JPEG หรือไม่มี EXIF จะคืนค่าว่าง
func ReadExif(path string) ExifInfo {
	f, err := os.Open(path)
	if err != nil {
		return ExifInfo{}
	}
	defer f.Close()
	// EXIF อยู่ต้นไฟล์ (APP1) อ่านแค่ 256KB พอ
	buf, _ := io.ReadAll(io.LimitReader(f, 256*1024))
	return parseExif(buf)
}

func parseExif(b []byte) ExifInfo {
	var info ExifInfo
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return info
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return info
		}
		marker := b[i+1]
		if marker == 0xD9 || marker == 0xDA { // EOI / SOS
			return info
		}
		if marker == 0xFF { // padding
			i++
			continue
		}
		segLen := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(b) {
			return info
		}
		if marker == 0xE1 {
			seg := b[i+4 : i+2+segLen]
			if bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
				parseTIFF(seg[6:], &info)
				return info
			}
		}
		i += 2 + segLen
	}
	return info
}

func parseTIFF(t []byte, info *ExifInfo) {
	if len(t) < 8 {
		return
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return
	}
	if bo.Uint16(t[2:4]) != 42 {
		return
	}
	ifd0 := int(bo.Uint32(t[4:8]))
	var exifPtr, gpsPtr int
	readIFD(t, ifd0, bo, func(tag uint16, typ uint16, count uint32, val []byte, raw uint32) {
		switch tag {
		case 0x010F:
			info.Make = cleanStr(readASCII(t, typ, count, val, raw, bo))
		case 0x0110:
			info.Model = cleanStr(readASCII(t, typ, count, val, raw, bo))
		case 0x0112:
			if typ == 3 {
				info.Orientation = int(bo.Uint16(val[:2]))
			}
		case 0x8769:
			exifPtr = int(raw)
		case 0x8825:
			gpsPtr = int(raw)
		}
	})
	if exifPtr > 0 {
		readIFD(t, exifPtr, bo, func(tag uint16, typ uint16, count uint32, val []byte, raw uint32) {
			if tag == 0x9003 || (tag == 0x9004 && !info.HasTaken) { // DateTimeOriginal / Digitized
				s := readASCII(t, typ, count, val, raw, bo)
				if tm, err := time.Parse("2006:01:02 15:04:05", cleanStr(s)); err == nil {
					info.Taken, info.HasTaken = tm, true
				}
			}
		})
	}
	if gpsPtr > 0 {
		n := 0
		readIFD(t, gpsPtr, bo, func(tag uint16, typ uint16, count uint32, val []byte, raw uint32) { n++ })
		info.HasGPS = n > 2
	}
}

func readIFD(t []byte, off int, bo binary.ByteOrder, fn func(tag, typ uint16, count uint32, val []byte, raw uint32)) {
	if off < 8 || off+2 > len(t) {
		return
	}
	n := int(bo.Uint16(t[off : off+2]))
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return
		}
		tag := bo.Uint16(t[e : e+2])
		typ := bo.Uint16(t[e+2 : e+4])
		count := bo.Uint32(t[e+4 : e+8])
		val := t[e+8 : e+12]
		fn(tag, typ, count, val, bo.Uint32(val))
	}
}

// readASCII อ่านค่า ASCII (type 2): ถ้า <=4 ไบต์อยู่ใน val, ไม่เช่นนั้น val คือ offset
func readASCII(t []byte, typ uint16, count uint32, val []byte, raw uint32, bo binary.ByteOrder) string {
	if typ != 2 || count == 0 {
		return ""
	}
	if count <= 4 {
		return string(val[:count])
	}
	start := int(raw)
	end := start + int(count)
	if start < 0 || end > len(t) || start > end {
		return ""
	}
	return string(t[start:end])
}

func cleanStr(s string) string {
	return strings.TrimSpace(strings.Trim(s, "\x00 "))
}
