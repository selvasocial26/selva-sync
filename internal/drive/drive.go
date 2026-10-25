// Package drive is a small Google Drive v3 client using only the Go standard
// library. It only needs the "drive.file" permission: Selva Sync can see and
// change only the files and folders it created itself.
package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const FolderMime = "application/vnd.google-apps.folder"

// SimpleUploadMax is the largest file sent in one request; bigger files use
// resumable uploads that continue after a broken connection.
const SimpleUploadMax = 5 << 20

// ChunkSize for resumable uploads (must be a multiple of 256 KiB).
var ChunkSize int64 = 8 << 20

// Client talks to the Drive REST API.
type Client struct {
	HTTP       *http.Client
	APIBase    string
	UploadBase string
	Token      func(ctx context.Context) (string, error)
	// Invalidate forces a fresh access token after a 401 response.
	Invalidate func()

	idMu  sync.Mutex
	ids   []string
	noIDs bool

	timeMu     sync.Mutex
	serverTime time.Time // Google's clock from the last response
	seenAt     time.Time // our clock when it was seen

	driveID atomic.Value // shared drive searched by List ("" = My Drive)
}

// SetSharedDrive makes searches look in this shared drive ("" = My Drive).
func (c *Client) SetSharedDrive(id string) { c.driveID.Store(id) }

func (c *Client) sharedDrive() string {
	s, _ := c.driveID.Load().(string)
	return s
}

// ErrIncomplete means Google answered a search with only part of the results.
var ErrIncomplete = errors.New("Google Drive returned incomplete search results")

// ServerNow estimates the current time using Google's clock, so a wrong PC
// clock cannot make old archives look expired. Falls back to the PC clock.
func (c *Client) ServerNow() time.Time {
	c.timeMu.Lock()
	defer c.timeMu.Unlock()
	if c.serverTime.IsZero() {
		return time.Now()
	}
	return c.serverTime.Add(time.Since(c.seenAt))
}

func (c *Client) noteServerTime(h http.Header) {
	if d := h.Get("Date"); d != "" {
		if t, err := http.ParseTime(d); err == nil {
			c.timeMu.Lock()
			c.serverTime, c.seenAt = t, time.Now()
			c.timeMu.Unlock()
		}
	}
}

// newID returns a file ID reserved in advance with files.generateIds. Using
// our own ID makes "create" requests safe to retry: if the first attempt
// reached Google but the answer was lost, the retry fails with 409 instead
// of creating a duplicate, and we know the file exists.
func (c *Client) newID(ctx context.Context) string {
	c.idMu.Lock()
	defer c.idMu.Unlock()
	if c.noIDs {
		return ""
	}
	if len(c.ids) == 0 {
		var res struct {
			IDs []string `json:"ids"`
		}
		_, err := c.request(ctx, "GET", c.APIBase+"/files/generateIds?count=200&space=drive&type=files", nil, nil, &res)
		if err != nil {
			var ae *APIError
			if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != 429 {
				c.noIDs = true // not supported: fall back to server-chosen IDs
			}
			return ""
		}
		c.ids = res.IDs
	}
	if len(c.ids) == 0 {
		return ""
	}
	id := c.ids[0]
	c.ids = c.ids[1:]
	return id
}

// isConflict reports that a file with our pre-reserved ID already exists,
// meaning an earlier attempt of the same create succeeded.
func isConflict(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Status == 409 || ae.Reason == "fileIdInUse" || ae.Reason == "duplicate")
}

// New returns a client for the real Google Drive.
func New(token func(ctx context.Context) (string, error)) *Client {
	return &Client{
		HTTP:       &http.Client{Timeout: 0},
		APIBase:    "https://www.googleapis.com/drive/v3",
		UploadBase: "https://www.googleapis.com/upload/drive/v3",
		Token:      token,
	}
}

// File is the subset of Drive file metadata Selva Sync uses.
type File struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	MimeType      string            `json:"mimeType"`
	Parents       []string          `json:"parents,omitempty"`
	Size          string            `json:"size,omitempty"`
	ModifiedTime  string            `json:"modifiedTime,omitempty"`
	Trashed       bool              `json:"trashed,omitempty"`
	AppProperties map[string]string `json:"appProperties,omitempty"`
	MD5           string            `json:"md5Checksum,omitempty"`
	CreatedTime   string            `json:"createdTime,omitempty"`
	DriveID       string            `json:"driveId,omitempty"`
	Capabilities  *struct {
		CanAddChildren bool `json:"canAddChildren"`
	} `json:"capabilities,omitempty"`
}

// Created returns when Drive created the file (Google's clock, not the PC's).
func (f File) Created() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, f.CreatedTime)
	return t
}

func (f File) IsFolder() bool { return f.MimeType == FolderMime }

func (f File) SizeInt() int64 { n, _ := strconv.ParseInt(f.Size, 10, 64); return n }

func (f File) ModTimeMs() int64 {
	t, err := time.Parse(time.RFC3339Nano, f.ModifiedTime)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

const fileFields = "id,name,mimeType,parents,size,modifiedTime,trashed,appProperties,md5Checksum,createdTime,driveId,capabilities/canAddChildren"

// APIError is returned for non-success HTTP responses.
type APIError struct {
	Status int
	Reason string
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("drive: HTTP %d %s", e.Status, e.Reason)
}

// IsNotFound reports a 404 from Drive.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == 404
}

// IsForbidden reports missing permission (e.g. access to a shared drive
// was removed), but not rate limits or a full Drive.
func IsForbidden(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == 403 && !IsQuota(err) &&
		ae.Reason != "rateLimitExceeded" && ae.Reason != "userRateLimitExceeded"
}

// IsAuth reports an authentication problem (user must sign in again).
func IsAuth(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == 401
}

// IsQuota reports that the Drive storage is full.
func IsQuota(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == 403 && (ae.Reason == "storageQuotaExceeded" || ae.Reason == "quotaExceeded")
}

func parseAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	ae := &APIError{Status: resp.StatusCode, Body: string(b)}
	var e struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Status string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil {
		if len(e.Error.Errors) > 0 {
			ae.Reason = e.Error.Errors[0].Reason
		} else {
			ae.Reason = e.Error.Status
		}
	}
	return ae
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case 408, 429, 500, 502, 503, 504:
			return true
		case 403:
			return ae.Reason == "rateLimitExceeded" || ae.Reason == "userRateLimitExceeded" || ae.Reason == "backendError"
		}
		return false
	}
	return true // network errors
}

// Sleep waits with context support.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<attempt) * time.Second
	if d > 32*time.Second {
		d = 32 * time.Second
	}
	return d + time.Duration(rand.Intn(1000))*time.Millisecond
}

// request performs one HTTP call with retries. body is a function so the
// request body can be rebuilt for each attempt.
func (c *Client) request(ctx context.Context, method, u string, body func() (io.Reader, string), headers map[string]string, out any) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			if err := Sleep(ctx, backoff(attempt-1)); err != nil {
				return nil, err
			}
		}
		resp, err := c.once(ctx, method, u, body, headers, out)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if IsAuth(err) && attempt == 0 && c.Invalidate != nil {
			c.Invalidate()
			continue
		}
		if !retryable(err) {
			return resp, err
		}
	}
	return nil, lastErr
}

// allDrives adds the parameters that let every files request work in
// shared drives as well as My Drive.
func allDrives(u string) string {
	if !strings.Contains(u, "/files") || strings.Contains(u, "/generateIds") || strings.Contains(u, "supportsAllDrives") {
		return u
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	u += sep + "supportsAllDrives=true"
	return u
}

func (c *Client) once(ctx context.Context, method, u string, body func() (io.Reader, string), headers map[string]string, out any) (*http.Response, error) {
	if strings.HasPrefix(u, c.APIBase) || strings.HasPrefix(u, c.UploadBase) {
		u = allDrives(u)
	}
	var r io.Reader
	ct := ""
	if body != nil {
		r, ct = body()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	c.noteServerTime(resp.Header)
	if resp.StatusCode == 308 { // resumable upload: incomplete
		io.Copy(io.Discard, resp.Body)
		return resp, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp, parseAPIError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
			return resp, err
		}
	} else {
		io.Copy(io.Discard, resp.Body)
	}
	return resp, nil
}

func jsonBody(v any) func() (io.Reader, string) {
	b, _ := json.Marshal(v)
	return func() (io.Reader, string) { return bytes.NewReader(b), "application/json; charset=UTF-8" }
}

func escapeQ(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `'`, `\'`)
}

// List returns all files matching a Drive query.
func (c *Client) List(ctx context.Context, q string) ([]File, error) {
	var all []File
	token := ""
	for {
		v := url.Values{}
		v.Set("q", q)
		v.Set("fields", "nextPageToken,files("+fileFields+")")
		v.Set("pageSize", "1000")
		v.Set("spaces", "drive")
		v.Set("includeItemsFromAllDrives", "true")
		if d := c.sharedDrive(); d != "" {
			// search only the shared drive that holds the backup folder
			v.Set("corpora", "drive")
			v.Set("driveId", d)
		}
		if token != "" {
			v.Set("pageToken", token)
		}
		var res struct {
			NextPageToken    string `json:"nextPageToken"`
			Files            []File `json:"files"`
			IncompleteSearch bool   `json:"incompleteSearch"`
		}
		if _, err := c.request(ctx, "GET", c.APIBase+"/files?"+v.Encode(), nil, nil, &res); err != nil {
			return nil, err
		}
		if res.IncompleteSearch {
			// acting on a partial list could upload duplicates: try later
			return nil, ErrIncomplete
		}
		all = append(all, res.Files...)
		if res.NextPageToken == "" {
			return all, nil
		}
		token = res.NextPageToken
	}
}

// Children lists the non-trashed items in a folder.
func (c *Client) Children(ctx context.Context, parentID string) ([]File, error) {
	return c.List(ctx, fmt.Sprintf("'%s' in parents and trashed=false", escapeQ(parentID)))
}

// FindChild returns the first non-trashed child with this name, or nil.
func (c *Client) FindChild(ctx context.Context, parentID, name string, folder bool) (*File, error) {
	q := fmt.Sprintf("'%s' in parents and name='%s' and trashed=false", escapeQ(parentID), escapeQ(name))
	if folder {
		q += " and mimeType='" + FolderMime + "'"
	}
	files, err := c.List(ctx, q)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	return &files[0], nil
}

// FindByAppProperty finds app-created folders tagged with a property.
func (c *Client) FindByAppProperty(ctx context.Context, key, value string) ([]File, error) {
	q := fmt.Sprintf("appProperties has { key='%s' and value='%s' } and trashed=false", escapeQ(key), escapeQ(value))
	return c.List(ctx, q)
}

// Get returns metadata for one file.
func (c *Client) Get(ctx context.Context, id string) (*File, error) {
	var f File
	_, err := c.request(ctx, "GET", c.APIBase+"/files/"+url.PathEscape(id)+"?fields="+fileFields, nil, nil, &f)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// CreateFolder makes a folder and returns its ID.
func (c *Client) CreateFolder(ctx context.Context, parentID, name string, props map[string]string) (string, error) {
	meta := map[string]any{"name": name, "mimeType": FolderMime}
	if parentID != "" {
		meta["parents"] = []string{parentID}
	}
	if len(props) > 0 {
		meta["appProperties"] = props
	}
	id := c.newID(ctx)
	if id != "" {
		meta["id"] = id
	}
	var f File
	_, err := c.request(ctx, "POST", c.APIBase+"/files?fields=id", jsonBody(meta), nil, &f)
	if err != nil && id != "" && isConflict(err) {
		return id, nil
	}
	return f.ID, err
}

// Rename changes a file or folder name.
func (c *Client) Rename(ctx context.Context, id, name string) error {
	_, err := c.request(ctx, "PATCH", c.APIBase+"/files/"+url.PathEscape(id)+"?fields=id", jsonBody(map[string]string{"name": name}), nil, nil)
	return err
}

// Move puts a file into another folder and optionally renames it (server
// side, no data transfer). The file's current parents are read from Drive,
// so it also works if the user moved the file on Drive.
func (c *Client) Move(ctx context.Context, id, toParent, newName string) error {
	f, err := c.Get(ctx, id)
	if err != nil {
		return err
	}
	v := url.Values{}
	remove := []string{}
	add := true
	for _, p := range f.Parents {
		if p == toParent {
			add = false
			continue
		}
		remove = append(remove, p)
	}
	if add {
		v.Set("addParents", toParent)
	}
	if len(remove) > 0 {
		v.Set("removeParents", strings.Join(remove, ","))
	}
	v.Set("fields", "id")
	meta := map[string]any{}
	if newName != "" && newName != f.Name {
		meta["name"] = newName
	}
	if !add && len(remove) == 0 && len(meta) == 0 {
		return nil
	}
	_, err = c.request(ctx, "PATCH", c.APIBase+"/files/"+url.PathEscape(id)+"?"+v.Encode(), jsonBody(meta), nil, nil)
	return err
}

// Delete removes an item permanently (only items Selva Sync created).
func (c *Client) Delete(ctx context.Context, id string) error {
	_, err := c.request(ctx, "DELETE", c.APIBase+"/files/"+url.PathEscape(id), nil, nil, nil)
	return err
}

// SetProps adds app properties to an item Selva Sync created.
func (c *Client) SetProps(ctx context.Context, id string, props map[string]string) error {
	_, err := c.request(ctx, "PATCH", c.APIBase+"/files/"+url.PathEscape(id)+"?fields=id", jsonBody(map[string]any{"appProperties": props}), nil, nil)
	return err
}

// Trashed lists every file and folder Selva Sync created that is now in
// the Drive trash (with the drive.file permission Google only returns
// Selva Sync's own items).
func (c *Client) Trashed(ctx context.Context) ([]File, error) {
	return c.List(ctx, "trashed=true")
}

// Copy duplicates a file on Drive (server side, no data transfer).
func (c *Client) Copy(ctx context.Context, id, name, parentID string) (string, error) {
	meta := map[string]any{"name": name, "parents": []string{parentID}}
	nid := c.newID(ctx)
	if nid != "" {
		meta["id"] = nid
	}
	var f File
	_, err := c.request(ctx, "POST", c.APIBase+"/files/"+url.PathEscape(id)+"/copy?fields=id", jsonBody(meta), nil, &f)
	if err != nil && nid != "" && isConflict(err) {
		return nid, nil
	}
	return f.ID, err
}

// Trash moves a file to the Drive trash (recoverable for 30 days).
func (c *Client) Trash(ctx context.Context, id string) error {
	_, err := c.request(ctx, "PATCH", c.APIBase+"/files/"+url.PathEscape(id)+"?fields=id", jsonBody(map[string]bool{"trashed": true}), nil, nil)
	return err
}

// About returns the account e-mail and storage numbers.
type About struct {
	User struct {
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
	} `json:"user"`
	StorageQuota struct {
		Limit string `json:"limit"`
		Usage string `json:"usage"`
	} `json:"storageQuota"`
}

func (c *Client) About(ctx context.Context) (*About, error) {
	var a About
	_, err := c.request(ctx, "GET", c.APIBase+"/about?fields=user(displayName,emailAddress),storageQuota(limit,usage)", nil, nil, &a)
	return &a, err
}

// UploadSpec describes one upload.
type UploadSpec struct {
	ExistingID string // update this file if set, else create
	ParentID   string
	Name       string
	Path       string // local file
	Size       int64
	ModTime    time.Time
	// Wrap lets the caller add bandwidth limiting and progress counting.
	Wrap func(io.Reader) io.Reader
	// Session holds a resumable session URL, so an upload can continue in a
	// later run. Updated by Upload; set to "" when done.
	Session *string
	// OnSession is called whenever a new session URL is created.
	OnSession func(url string)
	// MD5 receives Drive's checksum of the uploaded content, when known.
	MD5 *string
}

// Upload sends a local file to Drive and returns the Drive file ID.
func (c *Client) Upload(ctx context.Context, sp UploadSpec) (string, error) {
	if sp.Wrap == nil {
		sp.Wrap = func(r io.Reader) io.Reader { return r }
	}
	if sp.Size <= SimpleUploadMax {
		return c.simpleUpload(ctx, sp)
	}
	return c.resumableUpload(ctx, sp)
}

func (c *Client) metadata(sp UploadSpec) map[string]any {
	meta := map[string]any{"modifiedTime": sp.ModTime.UTC().Format(time.RFC3339Nano)}
	if sp.ExistingID == "" {
		meta["name"] = sp.Name
		meta["parents"] = []string{sp.ParentID}
	}
	return meta
}

func (c *Client) simpleUpload(ctx context.Context, sp UploadSpec) (string, error) {
	f, err := os.Open(sp.Path)
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, sp.Size+1))
	f.Close()
	if err != nil {
		return "", err
	}
	if int64(len(data)) != sp.Size {
		return "", ErrChanged
	}
	meta := c.metadata(sp)
	newID := ""
	if sp.ExistingID == "" {
		if newID = c.newID(ctx); newID != "" {
			meta["id"] = newID
		}
	}
	metaJSON, _ := json.Marshal(meta)
	boundary := fmt.Sprintf("selvasync%d", rand.Int63())
	body := func() (io.Reader, string) {
		var head bytes.Buffer
		mw := multipart.NewWriter(&head)
		mw.SetBoundary(boundary)
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", "application/json; charset=UTF-8")
		pw, _ := mw.CreatePart(h)
		pw.Write(metaJSON)
		h2 := textproto.MIMEHeader{}
		h2.Set("Content-Type", "application/octet-stream")
		mw.CreatePart(h2)
		tail := "\r\n--" + boundary + "--\r\n"
		r := io.MultiReader(&head, sp.Wrap(bytes.NewReader(data)), strings.NewReader(tail))
		return r, "multipart/related; boundary=" + boundary
	}
	method, u := "POST", c.UploadBase+"/files?uploadType=multipart&fields=id,md5Checksum"
	if sp.ExistingID != "" {
		method, u = "PATCH", c.UploadBase+"/files/"+url.PathEscape(sp.ExistingID)+"?uploadType=multipart&fields=id,md5Checksum"
	}
	var res File
	if _, err := c.request(ctx, method, u, body, nil, &res); err != nil {
		if newID != "" && isConflict(err) {
			return newID, nil
		}
		return "", err
	}
	if sp.MD5 != nil {
		*sp.MD5 = res.MD5
	}
	return res.ID, nil
}

// ErrChanged means the file changed while it was being uploaded.
var ErrChanged = errors.New("file changed during upload")

// ErrSessionGone means a saved resumable session expired.
var ErrSessionGone = errors.New("upload session expired")

func (c *Client) startSession(ctx context.Context, sp UploadSpec) (string, error) {
	method, u := "POST", c.UploadBase+"/files?uploadType=resumable&fields=id,md5Checksum"
	if sp.ExistingID != "" {
		method, u = "PATCH", c.UploadBase+"/files/"+url.PathEscape(sp.ExistingID)+"?uploadType=resumable&fields=id,md5Checksum"
	}
	meta := c.metadata(sp)
	if sp.ExistingID == "" {
		if id := c.newID(ctx); id != "" {
			meta["id"] = id
		}
	}
	resp, err := c.request(ctx, method, u, jsonBody(meta), map[string]string{
		"X-Upload-Content-Type":   "application/octet-stream",
		"X-Upload-Content-Length": strconv.FormatInt(sp.Size, 10),
	}, nil)
	if err != nil {
		return "", err
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("drive: no upload session URL")
	}
	return loc, nil
}

// sessionStatus asks how many bytes the server already has.
// Returns (offset, fileID if finished, error).
func (c *Client) sessionStatus(ctx context.Context, session string, size int64, md5out *string) (int64, string, error) {
	req, err := http.NewRequestWithContext(ctx, "PUT", session, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
	req.ContentLength = 0
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 200 || resp.StatusCode == 201:
		var f File
		json.NewDecoder(resp.Body).Decode(&f)
		if md5out != nil {
			*md5out = f.MD5
		}
		return size, f.ID, nil
	case resp.StatusCode == 308:
		return parseRange(resp.Header.Get("Range")), "", nil
	case resp.StatusCode == 404 || resp.StatusCode == 410:
		return 0, "", ErrSessionGone
	default:
		return 0, "", parseAPIError(resp)
	}
}

func parseRange(r string) int64 {
	// "bytes=0-1234" -> next offset 1235
	if r == "" {
		return 0
	}
	if i := strings.LastIndex(r, "-"); i >= 0 {
		n, err := strconv.ParseInt(r[i+1:], 10, 64)
		if err == nil {
			return n + 1
		}
	}
	return 0
}

func (c *Client) resumableUpload(ctx context.Context, sp UploadSpec) (string, error) {
	var local string
	if sp.Session == nil {
		sp.Session = &local
	}
	f, err := os.Open(sp.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() != sp.Size {
		return "", ErrChanged
	}

	var offset int64
	if *sp.Session != "" {
		off, id, err := c.sessionStatus(ctx, *sp.Session, sp.Size, sp.MD5)
		switch {
		case err == nil && id != "":
			*sp.Session = ""
			return id, nil
		case err == nil:
			offset = off
		default:
			*sp.Session = ""
		}
	}
	if *sp.Session == "" {
		s, err := c.startSession(ctx, sp)
		if err != nil {
			return "", err
		}
		*sp.Session = s
		if sp.OnSession != nil {
			sp.OnSession(s)
		}
		offset = 0
	}

	buf := make([]byte, ChunkSize)
	failures := 0
	for {
		n, err := f.ReadAt(buf[:min64(ChunkSize, sp.Size-offset)], offset)
		if err != nil && err != io.EOF {
			return "", err
		}
		if int64(n) != min64(ChunkSize, sp.Size-offset) {
			return "", ErrChanged
		}
		end := offset + int64(n) - 1
		req, _ := http.NewRequestWithContext(ctx, "PUT", *sp.Session, sp.Wrap(bytes.NewReader(buf[:n])))
		req.ContentLength = int64(n)
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end, sp.Size))
		resp, err := c.HTTP.Do(req)
		if err == nil {
			code := resp.StatusCode
			switch {
			case code == 200 || code == 201:
				var res File
				json.NewDecoder(resp.Body).Decode(&res)
				if sp.MD5 != nil {
					*sp.MD5 = res.MD5
				}
				resp.Body.Close()
				*sp.Session = ""
				return res.ID, nil
			case code == 308:
				offset = parseRange(resp.Header.Get("Range"))
				resp.Body.Close()
				failures = 0
				continue
			case code == 404 || code == 410:
				resp.Body.Close()
				*sp.Session = ""
				return "", ErrSessionGone
			default:
				err = parseAPIError(resp)
				resp.Body.Close()
				if !retryable(err) {
					return "", err
				}
			}
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		failures++
		if failures > 6 {
			return "", err
		}
		if e := Sleep(ctx, backoff(failures-1)); e != nil {
			return "", e
		}
		off, id, e := c.sessionStatus(ctx, *sp.Session, sp.Size, sp.MD5)
		if e == nil && id != "" {
			*sp.Session = ""
			return id, nil
		}
		if e == nil {
			offset = off
		}
	}
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
