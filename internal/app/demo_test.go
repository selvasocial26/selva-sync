package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/selvasync/selvasync/internal/auth"
	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/engine"
	"github.com/selvasync/selvasync/internal/fakedrive"
)

// TestDemoServer runs the real app window against a fake Google Drive so
// the interface can be checked in a browser. Only runs when SELVASYNC_DEMO=1.
func TestDemoServer(t *testing.T) {
	if os.Getenv("SELVASYNC_DEMO") == "" {
		t.Skip("set SELVASYNC_DEMO=1")
	}
	fd := fakedrive.New()
	defer fd.Close()
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "x", "expires_in": 3600})
	}))
	defer tok.Close()
	auth.TokenURL = tok.URL

	dir := t.TempDir()
	src := filepath.Join(dir, "Documents")
	for _, f := range []string{"Accounts/2026/ledger.xlsx", "Accounts/2026/vouchers.pdf", "Reports/annual-report.docx", "Photos/field-visit.jpg"} {
		p := filepath.Join(src, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, make([]byte, 150000), 0o644)
	}
	DefaultClientID = "demo.apps.googleusercontent.com"
	a, err := New(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SELVASYNC_DEMO_SIGNEDIN") != "" {
		b, _ := json.Marshal(map[string]string{"refresh_token": "r", "client_id": DefaultClientID})
		a.Store.WriteFile("token.bin", b)
		s := a.Settings()
		s.Sources = []config.Source{{ID: "1", Path: src, Enabled: true}, {ID: "2", Path: "E:\\", Enabled: true}}
		s.MonthlyCapMB = 5120
		s.DayLimitKBps = 512
		s.BlockedWifi = []string{"Selva-Phone"}
		a.SaveSettings(s)
	}
	if l := os.Getenv("SELVASYNC_DEMO_LANG"); l != "" {
		s := a.Settings()
		s.Language = l
		a.SaveSettings(s)
	}
	a.Drive.APIBase = fd.URL + "/drive/v3"
	a.Drive.UploadBase = fd.URL + "/upload/drive/v3"
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SELVASYNC_DEMO_V12") != "" {
		fd.AddFolder("sdTeam", "Utthan Team", "", "sdTeam")
		fd.AddFolder("sdBk", "PC Backups", "sdTeam", "sdTeam")
		a.mu.Lock()
		a.settings.PickerURL = "https://example.github.io/selva-sync/picker.html"
		a.settings.Location = config.Location{FolderID: "sdBk", Name: "Utthan Team › PC Backups", DriveID: "sdTeam"}
		a.settings.Managed = []string{"archiveDays", "deleteMode"}
		a.settings.OnlyOnAC = true
		a.mu.Unlock()
	}
	if os.Getenv("SELVASYNC_DEMO_SIGNEDIN") != "" {
		s := a.Settings()
		s.Sources = s.Sources[:1]
		a.SaveSettings(s)
		a.Engine.Run(context.Background(), engine.Options{Trigger: "manual"})
		s.Sources = append(s.Sources, config.Source{ID: "2", Path: "E:\\", Enabled: true})
		a.SaveSettings(s)
	}
	os.WriteFile(os.Getenv("SELVASYNC_DEMO_URLFILE"), []byte(a.URL()), 0o644)
	fmt.Println("DEMO", a.URL())
	time.Sleep(10 * time.Minute)
}
