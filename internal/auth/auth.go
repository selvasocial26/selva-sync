// Package auth signs in to Google with OAuth 2.0 (PKCE, loopback redirect)
// and keeps the refresh token encrypted on this computer.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/engine"
)

// Scope: only files and folders created by Selva Sync.
const Scope = "https://www.googleapis.com/auth/drive.file"

var (
	AuthURL   = "https://accounts.google.com/o/oauth2/v2/auth"
	TokenURL  = "https://oauth2.googleapis.com/token"
	RevokeURL = "https://oauth2.googleapis.com/revoke"
)

const tokenFile = "token.bin"

// Manager handles sign-in and access tokens.
type Manager struct {
	Store   *config.Store
	Protect func([]byte) ([]byte, error)
	Unprot  func([]byte) ([]byte, error)
	Creds   func() (id, secret string)
	HTTP    *http.Client

	mu        sync.Mutex
	refreshMu sync.Mutex // one token refresh at a time, network call outside mu
	hasToken  atomic.Bool
	refresh   string
	access    string
	expiry    time.Time
	pending   map[string]pendingLogin
	loaded    bool
	OnChange  func()
}

type pendingLogin struct {
	verifier string
	redirect string
	created  time.Time
}

type saved struct {
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
}

func (m *Manager) load() {
	if m.loaded {
		return
	}
	m.loaded = true
	b, err := m.Store.ReadFile(tokenFile)
	if err != nil {
		return
	}
	plain, err := m.Unprot(b)
	if err != nil {
		return
	}
	var s saved
	if json.Unmarshal(plain, &s) == nil {
		id, _ := m.Creds()
		if s.ClientID == "" || s.ClientID == id {
			m.refresh = s.RefreshToken
			m.hasToken.Store(m.refresh != "")
		}
	}
}

// SignedIn reports whether a Google account is linked. It never waits for
// the network, so the tray menu and window stay responsive.
func (m *Manager) SignedIn() bool {
	if m.hasToken.Load() {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.load()
	return m.refresh != ""
}

// Configured reports whether a Google client ID is available.
func (m *Manager) Configured() bool {
	id, _ := m.Creds()
	return id != ""
}

func randString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// BeginLogin returns the Google sign-in URL. redirect is the loopback
// callback URL served by the local app server.
func (m *Manager) BeginLogin(redirect string) (string, error) {
	id, _ := m.Creds()
	if id == "" {
		return "", errors.New("no Google client ID configured")
	}
	verifier := randString(48)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randString(24)
	m.mu.Lock()
	if m.pending == nil {
		m.pending = map[string]pendingLogin{}
	}
	for k, p := range m.pending {
		if time.Since(p.created) > 15*time.Minute {
			delete(m.pending, k)
		}
	}
	m.pending[state] = pendingLogin{verifier: verifier, redirect: redirect, created: time.Now()}
	m.mu.Unlock()
	v := url.Values{}
	v.Set("client_id", id)
	v.Set("redirect_uri", redirect)
	v.Set("response_type", "code")
	v.Set("scope", Scope)
	v.Set("access_type", "offline")
	v.Set("prompt", "consent select_account")
	v.Set("code_challenge", challenge)
	v.Set("code_challenge_method", "S256")
	v.Set("state", state)
	return AuthURL + "?" + v.Encode(), nil
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (m *Manager) post(ctx context.Context, form url.Values) (*tokenResp, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", TokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tr tokenResp
	json.NewDecoder(resp.Body).Decode(&tr)
	if resp.StatusCode != 200 {
		if tr.Error == "" {
			tr.Error = resp.Status
		}
		return &tr, fmt.Errorf("google: %s %s", tr.Error, tr.ErrorDesc)
	}
	return &tr, nil
}

// ValidState reports whether state belongs to a sign-in started here.
func (m *Manager) ValidState(state string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[state]
	return ok && time.Since(p.created) < 15*time.Minute
}

// FinishLogin exchanges the code from Google's redirect for tokens.
func (m *Manager) FinishLogin(ctx context.Context, state, code string) error {
	m.mu.Lock()
	p, ok := m.pending[state]
	delete(m.pending, state)
	m.mu.Unlock()
	if !ok {
		return errors.New("sign-in link expired, please try again")
	}
	id, secret := m.Creds()
	form := url.Values{}
	form.Set("client_id", id)
	if secret != "" {
		form.Set("client_secret", secret)
	}
	form.Set("code", code)
	form.Set("code_verifier", p.verifier)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", p.redirect)
	tr, err := m.post(ctx, form)
	if err != nil {
		return err
	}
	if tr.RefreshToken == "" {
		return errors.New("google did not return a refresh token")
	}
	m.mu.Lock()
	m.refresh = tr.RefreshToken
	m.hasToken.Store(true)
	m.access = tr.AccessToken
	m.expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	m.loaded = true
	m.mu.Unlock()
	b, _ := json.Marshal(saved{RefreshToken: tr.RefreshToken, ClientID: id})
	enc, err := m.Protect(b)
	if err != nil {
		return err
	}
	if err := m.Store.WriteFile(tokenFile, enc); err != nil {
		return err
	}
	if m.OnChange != nil {
		m.OnChange()
	}
	return nil
}

// Token returns a valid access token, refreshing it when needed. The
// network request is made without holding the main lock.
func (m *Manager) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	m.load()
	if m.refresh == "" {
		m.mu.Unlock()
		return "", engine.ErrNotSignedIn
	}
	if m.access != "" && time.Until(m.expiry) > time.Minute {
		tok := m.access
		m.mu.Unlock()
		return tok, nil
	}
	m.mu.Unlock()

	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	m.mu.Lock()
	refresh := m.refresh
	if refresh == "" {
		m.mu.Unlock()
		return "", engine.ErrNotSignedIn
	}
	if m.access != "" && time.Until(m.expiry) > time.Minute {
		tok := m.access // another caller refreshed meanwhile
		m.mu.Unlock()
		return tok, nil
	}
	m.mu.Unlock()

	id, secret := m.Creds()
	form := url.Values{}
	form.Set("client_id", id)
	if secret != "" {
		form.Set("client_secret", secret)
	}
	form.Set("refresh_token", refresh)
	form.Set("grant_type", "refresh_token")
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tr, err := m.post(rctx, form)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refresh != refresh {
		return "", engine.ErrNotSignedIn // signed out or changed meanwhile
	}
	if err != nil {
		if tr != nil && (tr.Error == "invalid_grant" || tr.Error == "unauthorized_client" || tr.Error == "invalid_client") {
			m.refresh, m.access = "", ""
			m.hasToken.Store(false)
			m.Store.Remove(tokenFile)
			if m.OnChange != nil {
				go m.OnChange()
			}
			return "", engine.ErrNotSignedIn
		}
		return "", err
	}
	m.access = tr.AccessToken
	m.expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return m.access, nil
}

// Invalidate forgets the cached access token.
func (m *Manager) Invalidate() {
	m.mu.Lock()
	m.access = ""
	m.mu.Unlock()
}

// SignOut revokes the token at Google and deletes it from this PC.
func (m *Manager) SignOut(ctx context.Context) {
	m.mu.Lock()
	m.load()
	tok := m.refresh
	m.refresh, m.access = "", ""
	m.hasToken.Store(false)
	m.mu.Unlock()
	m.Store.Remove(tokenFile)
	if tok != "" {
		req, _ := http.NewRequestWithContext(ctx, "POST", RevokeURL, strings.NewReader(url.Values{"token": {tok}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if resp, err := m.HTTP.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	if m.OnChange != nil {
		m.OnChange()
	}
}
