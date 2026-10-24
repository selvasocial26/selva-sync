package app

import (
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/selvasync/selvasync/internal/platform"
)

//go:embed web
var webFS embed.FS

// Language is one available translation.
type Language struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Dir  string `json:"dir"`
	User bool   `json:"user,omitempty"`
}

// I18n loads the built-in translations plus any language files the user
// placed in the "languages" folder (anyone can add a language by copying
// en.json, translating the text and saving it as <code>.json).
type I18n struct {
	UserDir string
	mu      sync.Mutex
	cache   map[string]map[string]string
}

func (i *I18n) raw(code string) ([]byte, bool) {
	code = strings.ToLower(code)
	if !validCode(code) {
		return nil, false
	}
	if i.UserDir != "" {
		if b, err := os.ReadFile(filepath.Join(i.UserDir, code+".json")); err == nil {
			return b, true
		}
	}
	b, err := webFS.ReadFile("web/i18n/" + code + ".json")
	return b, err == nil
}

func validCode(c string) bool {
	if len(c) < 2 || len(c) > 10 {
		return false
	}
	for _, r := range c {
		if !(r >= 'a' && r <= 'z' || r == '-' || r == '_' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Strings returns the merged strings for a language (English fills gaps).
func (i *I18n) Strings(code string) map[string]string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.cache == nil {
		i.cache = map[string]map[string]string{}
	}
	if m, ok := i.cache[code]; ok {
		return m
	}
	out := map[string]string{}
	if b, ok := i.raw("en"); ok {
		json.Unmarshal(b, &out)
	}
	if code != "en" {
		if b, ok := i.raw(code); ok {
			var m map[string]string
			if json.Unmarshal(b, &m) == nil {
				for k, v := range m {
					if strings.TrimSpace(v) != "" {
						out[k] = v
					}
				}
			}
		}
	}
	i.cache[code] = out
	return out
}

// Reload clears cached translations.
func (i *I18n) Reload() {
	i.mu.Lock()
	i.cache = nil
	i.mu.Unlock()
}

// T translates a key and fills {placeholders}.
func (i *I18n) T(code, key string, args map[string]string) string {
	s, ok := i.Strings(code)[key]
	if !ok {
		s = key
	}
	for k, v := range args {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// Has reports whether a language exists.
func (i *I18n) Has(code string) bool {
	_, ok := i.raw(code)
	return ok
}

// Languages lists built-in and user-added languages.
func (i *I18n) Languages() []Language {
	seen := map[string]bool{}
	var out []Language
	add := func(code string, b []byte, user bool) {
		if seen[code] {
			return
		}
		var m map[string]string
		if json.Unmarshal(b, &m) != nil {
			return
		}
		seen[code] = true
		name := m["_name"]
		if name == "" {
			name = code
		}
		dir := m["_dir"]
		if dir != "rtl" {
			dir = "ltr"
		}
		out = append(out, Language{Code: code, Name: name, Dir: dir, User: user})
	}
	if i.UserDir != "" {
		ents, _ := os.ReadDir(i.UserDir)
		for _, e := range ents {
			n := strings.ToLower(e.Name())
			if strings.HasSuffix(n, ".json") && validCode(strings.TrimSuffix(n, ".json")) {
				if b, err := os.ReadFile(filepath.Join(i.UserDir, e.Name())); err == nil {
					add(strings.TrimSuffix(n, ".json"), b, true)
				}
			}
		}
	}
	ents, _ := webFS.ReadDir("web/i18n")
	for _, e := range ents {
		code := strings.TrimSuffix(e.Name(), ".json")
		if b, err := webFS.ReadFile("web/i18n/" + e.Name()); err == nil {
			add(code, b, false)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Code == "en" {
			return true
		}
		if out[b].Code == "en" {
			return false
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// Translator returns a translation function using the saved language (or
// the Windows language), for messages shown before the app window exists.
func Translator(dataDir string) func(key string, args map[string]string) string {
	i := &I18n{UserDir: filepath.Join(dataDir, "languages")}
	lang := ""
	if b, err := os.ReadFile(filepath.Join(dataDir, "settings.json")); err == nil {
		var s struct {
			Language string `json:"language"`
		}
		if json.Unmarshal(b, &s) == nil {
			lang = s.Language
		}
	}
	if lang == "" || !i.Has(lang) {
		lang = platform.UserLanguage()
	}
	if !i.Has(lang) {
		lang = "en"
	}
	return func(key string, args map[string]string) string { return i.T(lang, key, args) }
}

// CompareVersions compares "1.2.3" style versions: -1, 0 or 1.
func CompareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(strings.TrimFunc(pa[i], func(r rune) bool { return r < '0' || r > '9' }))
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(strings.TrimFunc(pb[i], func(r rune) bool { return r < '0' || r > '9' }))
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
