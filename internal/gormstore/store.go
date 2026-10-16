// Package gormstore คือ core.Store ตัวจริง ใช้ GORM + SQLite
// (driver github.com/glebarez/sqlite เป็น pure Go ไม่ต้องมี CGO จึงข้ามแพลตฟอร์ม Windows/macOS ได้)
package gormstore

import (
	"errors"
	"fmt"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"image-backup-app/internal/core"
)

// Store เชื่อมต่อฐานข้อมูล SQLite ผ่าน GORM
type Store struct {
	db *gorm.DB
}

var _ core.Store = (*Store)(nil)

// Open เปิด (หรือสร้าง) ฐานข้อมูลที่ path แล้ว AutoMigrate ตาราง
func Open(path string) (*Store, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("เชื่อมต่อฐานข้อมูล SQLite ไม่ได้: %w", err)
	}
	// SQLite เขียนได้ทีละ connection: ใช้ 1 connection กัน "database is locked"
	// เมื่อ worker วิเคราะห์ภาพหลายตัวบันทึกผลพร้อมกัน
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&core.Image{}, &core.Tag{}, &core.Settings{}); err != nil {
		return nil, fmt.Errorf("สร้างตารางไม่สำเร็จ: %w", err)
	}
	return &Store{db: db}, nil
}

// ===================== settings =====================

func (s *Store) LoadSettings() (core.Settings, bool, error) {
	var rows []core.Settings
	if err := s.db.Order("id desc").Limit(1).Find(&rows).Error; err != nil {
		return core.Settings{}, false, err
	}
	if len(rows) == 0 {
		return core.Settings{}, false, nil
	}
	return rows[0], true, nil
}

// SaveSettings เป็น upsert: มีแถวเดียวเสมอ (เหมือนตาราง settings งานกลางภาค)
func (s *Store) SaveSettings(st core.Settings) error {
	var rows []core.Settings
	if err := s.db.Order("id desc").Limit(1).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) > 0 {
		st.ID = rows[0].ID
	}
	return s.db.Save(&st).Error
}

// ===================== images =====================

func (s *Store) first(cond string, args ...any) (*core.Image, error) {
	var rows []core.Image
	if err := s.db.Preload("Tags").Where(cond, args...).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

func (s *Store) FindImage(dest, filename string) (*core.Image, error) {
	return s.first("dest = ? AND filename = ?", dest, filename)
}

func (s *Store) FindByHash(dest, hash string) (*core.Image, error) {
	if hash == "" {
		return nil, nil
	}
	return s.first("dest = ? AND hash = ?", dest, hash)
}

func (s *Store) GetImage(id uint) (*core.Image, error) {
	return s.first("id = ?", id)
}

// resolveTags หา/สร้าง Tag ตามชื่อ (ชื่อไม่ซ้ำ) ภายใน transaction
func resolveTags(tx *gorm.DB, names []string) ([]core.Tag, error) {
	out := make([]core.Tag, 0, len(names))
	for _, n := range core.NormalizeTags(names) {
		var t core.Tag
		if err := tx.Where(core.Tag{Name: n}).FirstOrCreate(&t).Error; err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *Store) CreateImage(img *core.Image, tags []string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		ts, err := resolveTags(tx, tags)
		if err != nil {
			return err
		}
		img.Tags = ts
		return tx.Create(img).Error
	})
}

func (s *Store) UpdateAnalysis(id uint, description, analyzer string, width, height int, tags []string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Model(&core.Image{}).Where("id = ?", id).Updates(map[string]any{
			"description": description,
			"analyzer":    analyzer,
			"width":       width,
			"height":      height,
		}).Error
		if err != nil {
			return err
		}
		ts, err := resolveTags(tx, tags)
		if err != nil {
			return err
		}
		return tx.Model(&core.Image{ID: id}).Association("Tags").Replace(ts)
	})
}

func (s *Store) DeleteImage(dest, filename string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var rows []core.Image
		if err := tx.Where("dest = ? AND filename = ?", dest, filename).Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			if err := tx.Model(&core.Image{ID: r.ID}).Association("Tags").Clear(); err != nil {
				return err
			}
			if err := tx.Delete(&core.Image{}, r.ID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) ListImages(dest string) ([]core.Image, error) {
	var rows []core.Image
	err := s.db.Preload("Tags").Where("dest = ?", dest).Order("id asc").Find(&rows).Error
	return rows, err
}

// escapeLike ทำให้ % _ \ ในคำค้นเป็นตัวอักษรธรรมดา
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// Search: ทุกคำ (keywords) ต้องปรากฏในคำอธิบายหรือชื่อไฟล์, และถ้าระบุ tag ต้องมี tag นั้น
func (s *Store) Search(dest string, keywords []string, tag string) ([]core.Image, error) {
	q := s.db.Preload("Tags").Where("dest = ?", dest)
	for _, kw := range keywords {
		pat := "%" + escapeLike(kw) + "%"
		q = q.Where(`(LOWER(description) LIKE ? ESCAPE '\' OR LOWER(filename) LIKE ? ESCAPE '\')`, pat, pat)
	}
	if tag != "" {
		sub := s.db.Table("image_tags").
			Select("image_tags.image_id").
			Joins("JOIN tags ON tags.id = image_tags.tag_id").
			Where("tags.name = ?", tag)
		q = q.Where("id IN (?)", sub)
	}
	var rows []core.Image
	err := q.Order("id desc").Find(&rows).Error
	return rows, err
}

// TagCounts คืนจำนวนรูปต่อ Tag ภายใน Destination (ใช้วาด Tag Cloud)
func (s *Store) TagCounts(dest string) ([]core.TagCount, error) {
	out := []core.TagCount{}
	err := s.db.Raw(`
		SELECT tags.name AS name, COUNT(*) AS count
		FROM tags
		JOIN image_tags ON image_tags.tag_id = tags.id
		JOIN images ON images.id = image_tags.image_id
		WHERE images.dest = ?
		GROUP BY tags.name
		ORDER BY count DESC, tags.name ASC`, dest).Scan(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, nil
	}
	return out, err
}
