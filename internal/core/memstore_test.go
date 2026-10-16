package core

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// memStore คือ Store จำลองในหน่วยความจำ ใช้ทดสอบตรรกะโดยไม่ต้องมี SQLite
type memStore struct {
	mu       sync.Mutex
	settings *Settings
	images   map[uint]*Image
	next     uint
}

func newMemStore() *memStore { return &memStore{images: map[uint]*Image{}, next: 1} }

func (m *memStore) LoadSettings() (Settings, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings == nil {
		return Settings{}, false, nil
	}
	return *m.settings, true, nil
}
func (m *memStore) SaveSettings(s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := s
	m.settings = &c
	return nil
}
func (m *memStore) FindImage(dest, f string) (*Image, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, i := range m.images {
		if i.Dest == dest && i.Filename == f {
			c := *i
			return &c, nil
		}
	}
	return nil, nil
}
func (m *memStore) FindByHash(dest, h string) (*Image, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, i := range m.images {
		if i.Dest == dest && i.Hash == h {
			c := *i
			return &c, nil
		}
	}
	return nil, nil
}
func (m *memStore) GetImage(id uint) (*Image, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i, ok := m.images[id]; ok {
		c := *i
		return &c, nil
	}
	return nil, nil
}
func toTags(names []string) []Tag {
	var t []Tag
	for _, n := range NormalizeTags(names) {
		t = append(t, Tag{Name: n})
	}
	return t
}
func (m *memStore) CreateImage(img *Image, tags []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	img.ID = m.next
	m.next++
	img.CreatedAt = time.Now()
	img.Tags = toTags(tags)
	c := *img
	m.images[img.ID] = &c
	return nil
}
func (m *memStore) UpdateAnalysis(id uint, d, a string, w, h int, tags []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.images[id]
	i.Description, i.Analyzer, i.Width, i.Height = d, a, w, h
	i.Tags = toTags(tags)
	return nil
}
func (m *memStore) DeleteImage(dest, f string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, i := range m.images {
		if i.Dest == dest && i.Filename == f {
			delete(m.images, id)
		}
	}
	return nil
}
func (m *memStore) ListImages(dest string) ([]Image, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Image{}
	for _, i := range m.images {
		if i.Dest == dest {
			out = append(out, *i)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}
func (m *memStore) Search(dest string, kws []string, tag string) ([]Image, error) {
	all, _ := m.ListImages(dest)
	out := []Image{}
	for _, i := range all {
		ok := true
		for _, k := range kws {
			if !strings.Contains(strings.ToLower(i.Description), k) && !strings.Contains(strings.ToLower(i.Filename), k) {
				ok = false
			}
		}
		if tag != "" {
			has := false
			for _, t := range i.Tags {
				if t.Name == tag {
					has = true
				}
			}
			ok = ok && has
		}
		if ok {
			out = append(out, i)
		}
	}
	return out, nil
}
func (m *memStore) TagCounts(dest string) ([]TagCount, error) {
	all, _ := m.ListImages(dest)
	c := map[string]int{}
	for _, i := range all {
		for _, t := range i.Tags {
			c[t.Name]++
		}
	}
	out := []TagCount{}
	for k, v := range c {
		out = append(out, TagCount{k, v})
	}
	return out, nil
}
