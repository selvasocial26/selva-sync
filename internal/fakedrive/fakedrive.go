// Package fakedrive is an in-memory imitation of the Google Drive API used
// only by automated tests. It is never part of the Selva Sync program.
package fakedrive

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type File struct {
	Created  string
	ID       string
	Name     string
	Mime     string
	Parents  []string
	Data     []byte
	Modified string
	Trashed  bool
	Props    map[string]string
}

type session struct {
	fileID string // update target, "" for create
	meta   map[string]any
	size   int64
	data   []byte
}

// Server is a fake Drive.
type Server struct {
	mu       sync.Mutex
	Files    map[string]*File
	sessions map[string]*session
	next     int
	*httptest.Server
	// FailChunkOnce makes the next resumable chunk PUT fail with 503 after
	// storing nothing, to test resuming.
	FailChunkOnce bool
	Requests      int
	UploadedBytes int64
	// DropCreateOnce commits the next create but answers 503, as if the
	// network dropped the reply.
	DropCreateOnce bool
	// Clock, if set, is the server's time (for testing a wrong PC clock).
	Clock time.Time
	// GenerateIDs enables files.generateIds support.
	GenerateIDs bool

	// ---- break_test extensions ----
	// FailUploadStatus, when non-zero, makes multipart uploads and resumable
	// chunks after the first one answer with this HTTP status (non-retryable
	// codes such as 400 model a persistent failure).
	FailUploadStatus int
	// AfterCommit is called (with the server lock released) after an upload
	// has been stored, before the reply is sent.
	AfterCommit func(name string)
	// StrictParents models real Drive since 2020: a file may have only one
	// parent; a PATCH leaving it with more than one fails with 403.
	StrictParents bool

	// ---- break2_test extensions ----
	// CaseInsensitiveNames makes name='x' queries match regardless of case
	// (believed to match real Drive query behaviour).
	CaseInsensitiveNames bool
}

func conflict(w http.ResponseWriter) {
	write(w, 409, map[string]any{"error": map[string]any{"errors": []map[string]string{{"reason": "fileIdInUse"}}}})
}

// dropped reports (once) that a committed create should answer 503.
func (s *Server) dropped(w http.ResponseWriter) bool {
	if s.DropCreateOnce {
		s.DropCreateOnce = false
		write(w, 503, map[string]any{"error": map[string]any{"status": "UNAVAILABLE"}})
		return true
	}
	return false
}

func New() *Server {
	s := &Server{Files: map[string]*File{}, sessions: map[string]*session{}}
	s.Files["root"] = &File{ID: "root", Name: "My Drive", Mime: "application/vnd.google-apps.folder"}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *Server) newID() string { s.next++; return fmt.Sprintf("f%05d", s.next) }

func (s *Server) json(f *File) map[string]any {
	m := map[string]any{"id": f.ID, "name": f.Name, "mimeType": f.Mime, "parents": f.Parents, "trashed": s.gone(f), "modifiedTime": f.Modified, "createdTime": f.Created}
	if f.Mime != "application/vnd.google-apps.folder" {
		m["size"] = strconv.Itoa(len(f.Data))
		sum := md5.Sum(f.Data)
		m["md5Checksum"] = hex.EncodeToString(sum[:])
	}
	if f.Props != nil {
		m["appProperties"] = f.Props
	}
	return m
}

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	write(w, 404, map[string]any{"error": map[string]any{"errors": []map[string]string{{"reason": "notFound"}}}})
}

var (
	reParent = regexp.MustCompile(`'([^']+)' in parents`)
	reName   = regexp.MustCompile(`name='((?:[^'\\]|\\.)*)'`)
	reProp   = regexp.MustCompile(`appProperties has \{ key='([^']+)' and value='([^']+)' \}`)
)

func unesc(s string) string { return strings.NewReplacer(`\'`, `'`, `\\`, `\`).Replace(s) }

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests++
	now := time.Now()
	if !s.Clock.IsZero() {
		now = s.Clock
	}
	w.Header().Set("Date", now.UTC().Format(http.TimeFormat))
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && !strings.HasPrefix(r.URL.Path, "/upload/session/") {
		write(w, 401, map[string]any{"error": map[string]any{"status": "UNAUTHENTICATED"}})
		return
	}
	p := r.URL.Path
	q := r.URL.Query()
	switch {
	case p == "/drive/v3/about":
		write(w, 200, map[string]any{"user": map[string]string{"emailAddress": "test@example.com", "displayName": "Test"}, "storageQuota": map[string]string{"limit": "16106127360", "usage": "1000"}})
	case p == "/drive/v3/files" && r.Method == "GET":
		query := q.Get("q")
		var out []map[string]any
		for _, f := range s.Files {
			if f.ID == "root" {
				continue
			}
			if strings.Contains(query, "trashed=false") && s.gone(f) {
				continue
			}
			if strings.Contains(query, "trashed=true") && !s.gone(f) {
				continue
			}
			if m := reParent.FindStringSubmatch(query); m != nil && !contains(f.Parents, m[1]) {
				continue
			}
			if m := reName.FindStringSubmatch(query); m != nil && f.Name != unesc(m[1]) && !(s.CaseInsensitiveNames && strings.EqualFold(f.Name, unesc(m[1]))) {
				continue
			}
			if strings.Contains(query, "mimeType='application/vnd.google-apps.folder'") && f.Mime != "application/vnd.google-apps.folder" {
				continue
			}
			if m := reProp.FindStringSubmatch(query); m != nil && (f.Props == nil || f.Props[m[1]] != m[2]) {
				continue
			}
			out = append(out, s.json(f))
		}
		write(w, 200, map[string]any{"files": out})
	case p == "/drive/v3/files/generateIds":
		if !s.GenerateIDs {
			write(w, 403, map[string]any{"error": map[string]any{"errors": []map[string]string{{"reason": "forbidden"}}}})
			return
		}
		n, _ := strconv.Atoi(q.Get("count"))
		ids := []string{}
		for i := 0; i < n; i++ {
			ids = append(ids, "g"+s.newID())
		}
		write(w, 200, map[string]any{"ids": ids})
	case p == "/drive/v3/files" && r.Method == "POST":
		var meta map[string]any
		json.NewDecoder(r.Body).Decode(&meta)
		f, dup := s.create(meta)
		if dup {
			conflict(w)
			return
		}
		if s.dropped(w) {
			return
		}
		write(w, 200, map[string]any{"id": f.ID})
	case strings.HasPrefix(p, "/drive/v3/files/") && strings.HasSuffix(p, "/copy"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/drive/v3/files/"), "/copy")
		src, ok := s.Files[id]
		if !ok {
			notFound(w)
			return
		}
		var meta map[string]any
		json.NewDecoder(r.Body).Decode(&meta)
		cid := s.newID()
		if v, ok := meta["id"].(string); ok && v != "" {
			if _, exists := s.Files[v]; exists {
				conflict(w)
				return
			}
			cid = v
		}
		c := &File{ID: cid, Name: src.Name, Mime: src.Mime, Data: append([]byte(nil), src.Data...), Modified: src.Modified}
		if n, ok := meta["name"].(string); ok {
			c.Name = n
		}
		c.Parents = toStrings(meta["parents"])
		s.Files[c.ID] = c
		write(w, 200, map[string]any{"id": c.ID})
	case strings.HasPrefix(p, "/drive/v3/files/"):
		id := strings.TrimPrefix(p, "/drive/v3/files/")
		f, ok := s.Files[id]
		if !ok {
			notFound(w)
			return
		}
		if r.Method == "GET" {
			write(w, 200, s.json(f))
			return
		}
		if r.Method == "DELETE" {
			var drop func(id string)
			drop = func(id string) {
				for _, c := range s.Files {
					for _, p := range c.Parents {
						if p == id {
							drop(c.ID)
						}
					}
				}
				delete(s.Files, id)
			}
			drop(id)
			w.WriteHeader(204)
			return
		}
		if r.Method == "PATCH" {
			var meta map[string]any
			json.NewDecoder(r.Body).Decode(&meta)
			if n, ok := meta["name"].(string); ok {
				f.Name = n
			}
			if t, ok := meta["trashed"].(bool); ok {
				f.Trashed = t
			}
			if pr, ok := meta["appProperties"].(map[string]any); ok {
				if f.Props == nil {
					f.Props = map[string]string{}
				}
				for k, v := range pr {
					f.Props[k] = fmt.Sprint(v)
				}
			}
			parents := append([]string(nil), f.Parents...)
			if rm := q.Get("removeParents"); rm != "" {
				drop := map[string]bool{}
				for _, x := range strings.Split(rm, ",") {
					drop[x] = true
				}
				var np []string
				for _, x := range parents {
					if !drop[x] {
						np = append(np, x)
					}
				}
				parents = np
			}
			if ad := q.Get("addParents"); ad != "" {
				parents = append(parents, ad)
			}
			if s.StrictParents && len(parents) > 1 {
				write(w, 403, map[string]any{"error": map[string]any{"errors": []map[string]string{{"reason": "cannotAddParent"}}}})
				return
			}
			f.Parents = parents
			write(w, 200, map[string]any{"id": f.ID})
			return
		}
	case strings.HasPrefix(p, "/upload/drive/v3/files"):
		s.upload(w, r)
	case strings.HasPrefix(p, "/upload/session/"):
		s.chunk(w, r)
	default:
		notFound(w)
	}
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func toStrings(v any) []string {
	var out []string
	if arr, ok := v.([]any); ok {
		for _, x := range arr {
			out = append(out, fmt.Sprint(x))
		}
	}
	return out
}

func (s *Server) create(meta map[string]any) (*File, bool) {
	id := s.newID()
	if v, ok := meta["id"].(string); ok && v != "" {
		if _, exists := s.Files[v]; exists {
			return nil, true
		}
		id = v
	}
	now := time.Now()
	if !s.Clock.IsZero() {
		now = s.Clock
	}
	f := &File{ID: id, Modified: now.UTC().Format(time.RFC3339Nano), Created: now.UTC().Format(time.RFC3339Nano)}
	s.applyMeta(f, meta)
	if len(f.Parents) == 0 {
		f.Parents = []string{"root"}
	}
	s.Files[f.ID] = f
	return f, false
}

func (s *Server) applyMeta(f *File, meta map[string]any) {
	if n, ok := meta["name"].(string); ok {
		f.Name = n
	}
	if m, ok := meta["mimeType"].(string); ok {
		f.Mime = m
	}
	if f.Mime == "" {
		f.Mime = "application/octet-stream"
	}
	if ps := toStrings(meta["parents"]); len(ps) > 0 {
		f.Parents = ps
	}
	if mt, ok := meta["modifiedTime"].(string); ok {
		f.Modified = mt
	}
	if pr, ok := meta["appProperties"].(map[string]any); ok {
		f.Props = map[string]string{}
		for k, v := range pr {
			f.Props[k] = fmt.Sprint(v)
		}
	}
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/upload/drive/v3/files"), "/")
	var target *File
	if id != "" {
		var ok bool
		if target, ok = s.Files[id]; !ok {
			notFound(w)
			return
		}
	}
	switch r.URL.Query().Get("uploadType") {
	case "multipart":
		if s.FailUploadStatus != 0 {
			io.Copy(io.Discard, r.Body)
			write(w, s.FailUploadStatus, map[string]any{"error": map[string]any{"errors": []map[string]string{{"reason": "badRequest"}}}})
			return
		}
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		part, _ := mr.NextPart()
		var meta map[string]any
		json.NewDecoder(part).Decode(&meta)
		part, _ = mr.NextPart()
		data, _ := io.ReadAll(part)
		s.UploadedBytes += int64(len(data))
		if target == nil {
			var dup bool
			if target, dup = s.create(meta); dup {
				conflict(w)
				return
			}
			target.Data = data
			if s.dropped(w) {
				return
			}
		} else {
			s.applyMeta(target, meta)
			target.Data = data
		}
		s.afterCommit(target.Name)
		write(w, 200, map[string]any{"id": target.ID, "md5Checksum": md5hex(target.Data)})
	case "resumable":
		var meta map[string]any
		json.NewDecoder(r.Body).Decode(&meta)
		size, _ := strconv.ParseInt(r.Header.Get("X-Upload-Content-Length"), 10, 64)
		sid := s.newID()
		s.sessions[sid] = &session{fileID: id, meta: meta, size: size}
		w.Header().Set("Location", s.URL+"/upload/session/"+sid)
		w.WriteHeader(200)
	default:
		write(w, 400, map[string]string{"error": "bad uploadType"})
	}
}

func (s *Server) chunk(w http.ResponseWriter, r *http.Request) {
	sid := strings.TrimPrefix(r.URL.Path, "/upload/session/")
	se, ok := s.sessions[sid]
	if !ok {
		notFound(w)
		return
	}
	cr := r.Header.Get("Content-Range")
	if strings.HasPrefix(cr, "bytes */") {
		if int64(len(se.data)) == se.size && se.size > 0 {
			s.finish(w, sid, se)
			return
		}
		if len(se.data) > 0 {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(se.data)-1))
		}
		w.WriteHeader(308)
		return
	}
	var start, end, total int64
	fmt.Sscanf(cr, "bytes %d-%d/%d", &start, &end, &total)
	body, _ := io.ReadAll(r.Body)
	if s.FailUploadStatus != 0 && start > 0 {
		write(w, s.FailUploadStatus, map[string]any{"error": map[string]any{"errors": []map[string]string{{"reason": "badRequest"}}}})
		return
	}
	if s.FailChunkOnce && start > 0 {
		s.FailChunkOnce = false
		write(w, 503, map[string]any{"error": map[string]any{"status": "UNAVAILABLE"}})
		return
	}
	if start != int64(len(se.data)) {
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(se.data)-1))
		w.WriteHeader(308)
		return
	}
	se.data = append(se.data, body...)
	s.UploadedBytes += int64(len(body))
	if int64(len(se.data)) >= se.size {
		s.finish(w, sid, se)
		return
	}
	w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(se.data)-1))
	w.WriteHeader(308)
}

func (s *Server) finish(w http.ResponseWriter, sid string, se *session) {
	var f *File
	if se.fileID != "" {
		f = s.Files[se.fileID]
		s.applyMeta(f, se.meta)
	} else {
		var dup bool
		if f, dup = s.create(se.meta); dup {
			conflict(w)
			return
		}
	}
	f.Data = bytes.Clone(se.data)
	s.afterCommit(f.Name)
	write(w, 200, map[string]any{"id": f.ID, "md5Checksum": md5hex(f.Data)})
}

// Path returns "A/B/c.txt" style path of a file by following parents.
func (s *Server) Path(f *File) string {
	parts := []string{f.Name}
	cur := f
	for len(cur.Parents) > 0 && cur.Parents[0] != "root" {
		p, ok := s.Files[cur.Parents[0]]
		if !ok {
			break
		}
		parts = append([]string{p.Name}, parts...)
		cur = p
	}
	return strings.Join(parts, "/")
}

// gone reports whether a file or any of its parent folders is trashed.
func (s *Server) gone(f *File) bool {
	for cur := f; cur != nil; {
		if cur.Trashed {
			return true
		}
		if len(cur.Parents) == 0 || cur.Parents[0] == "root" {
			return false
		}
		cur = s.Files[cur.Parents[0]]
	}
	return false
}

// Live returns all non-trashed files (not folders) by path.
func (s *Server) Live() map[string][]*File {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]*File{}
	for _, f := range s.Files {
		if f.ID == "root" || s.gone(f) || f.Mime == "application/vnd.google-apps.folder" {
			continue
		}
		out[s.Path(f)] = append(out[s.Path(f)], f)
	}
	return out
}

// Folders returns all non-trashed folder paths with counts (to find duplicates).
func (s *Server) Folders() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, f := range s.Files {
		if f.ID != "root" && !s.gone(f) && f.Mime == "application/vnd.google-apps.folder" {
			out[s.Path(f)]++
		}
	}
	return out
}

func (s *Server) afterCommit(name string) {
	if s.AfterCommit != nil {
		s.mu.Unlock()
		s.AfterCommit(name)
		s.mu.Lock()
	}
}

// Lock / Unlock give tests safe direct access to Files.
func (s *Server) Lock()   { s.mu.Lock() }
func (s *Server) Unlock() { s.mu.Unlock() }

func md5hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}
