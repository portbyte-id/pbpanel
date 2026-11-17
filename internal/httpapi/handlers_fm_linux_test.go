//go:build linux

package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/fm"
	"github.com/portbyte-id/server-panel/internal/jobs"
)

type fmHarness struct {
	*harness
	s    session
	work string
	svc  *fm.Service
}

func newFMHarness(t *testing.T) *fmHarness {
	t.Helper()
	h := newHarness(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	svc, err := fm.New(fm.Options{Store: h.store, PanelDir: filepath.Join(root, "panel")})
	if err != nil {
		t.Fatal(err)
	}
	h.deps.Files = svc
	if h.handler, err = New(h.deps); err != nil {
		t.Fatal(err)
	}
	return &fmHarness{harness: h, s: h.login(), work: work, svc: svc}
}

func (h *fmHarness) call(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	rec := h.do(method, path, h.s.cookie, h.s.csrf, body)
	var out map[string]any
	if rec.Body.Len() > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		h.decode(rec, &out)
	}
	return rec.Code, out
}

func (h *fmHarness) p(rel string) string { return filepath.Join(h.work, rel) }

func (h *fmHarness) waitJob(out map[string]any) jobs.Job {
	h.t.Helper()
	job, ok := out["job"].(map[string]any)
	if !ok {
		h.t.Fatalf("no job in %v", out)
	}
	done, err := h.deps.Jobs.Wait(context.Background(), job["id"].(string))
	if err != nil {
		h.t.Fatal(err)
	}
	return done
}

func TestFMRoutesNeedAuthAndAnswer503WithoutFiles(t *testing.T) {
	h := newHarness(t)
	if rec := h.do(http.MethodGet, "/api/fm/list?path=/", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("list without a session = %d", rec.Code)
	}
	s := h.login()
	if rec := h.do(http.MethodGet, "/api/fm/list?path=/", s.cookie, s.csrf, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("list without a file manager = %d", rec.Code)
	}
	// An unsafe method without the CSRF token is refused before any handler runs.
	f := newFMHarness(t)
	if rec := f.do(http.MethodPost, "/api/fm/delete", f.s.cookie, "", map[string]any{"items": []any{}}); rec.Code != http.StatusForbidden {
		t.Errorf("delete without CSRF = %d", rec.Code)
	}
}

func TestFMBrowseAndGuardrails(t *testing.T) {
	h := newFMHarness(t)
	_ = os.WriteFile(h.p("a.txt"), []byte("hello"), 0o644)
	code, out := h.call(http.MethodGet, "/api/fm/list?path="+url.QueryEscape(h.work), nil)
	if code != http.StatusOK || out["total"].(float64) != 1 {
		t.Fatalf("list = %d %v", code, out)
	}
	if code, out := h.call(http.MethodGet, "/api/fm/list?path=relative", nil); code != http.StatusBadRequest || out["error"] != errInvalidInput {
		t.Errorf("relative path = %d %v", code, out)
	}
	if code, out := h.call(http.MethodGet, "/api/fm/list?path=/nope/nothing", nil); code != http.StatusNotFound {
		t.Errorf("missing dir = %d %v", code, out)
	}

	code, out = h.call(http.MethodPost, "/api/fm/mkdir", map[string]any{"path": h.p("sub")})
	if code != http.StatusCreated || out["kind"] != "dir" {
		t.Fatalf("mkdir = %d %v", code, out)
	}
	if code, out = h.call(http.MethodPost, "/api/fm/mkdir", map[string]any{"path": h.p("sub")}); code != http.StatusConflict {
		t.Errorf("mkdir again = %d %v", code, out)
	}
	if code, out = h.call(http.MethodPost, "/api/fm/rename", map[string]any{"path": h.p("a.txt"), "name": "b.txt"}); code != http.StatusOK || out["name"] != "b.txt" {
		t.Errorf("rename = %d %v", code, out)
	}

	// Exit gate: deleting /etc is refused, and so is anything under /proc.
	for _, p := range []string{"/etc", "/etc/", "//etc", "/", "/usr"} {
		code, out = h.call(http.MethodPost, "/api/fm/delete", map[string]any{"items": []any{map[string]any{"path": p, "confirm": "etc"}}})
		if code != http.StatusForbidden || out["error"] != errProtected {
			t.Errorf("delete %q = %d %v", p, code, out)
		}
	}
	if code, out = h.call(http.MethodPost, "/api/fm/mkdir", map[string]any{"path": "/proc/x"}); code != http.StatusForbidden {
		t.Errorf("mkdir in /proc = %d %v", code, out)
	}

	// A recursive delete without the typed name is refused; with it, it works.
	_ = os.WriteFile(h.p("sub/in.txt"), []byte("x"), 0o644)
	code, out = h.call(http.MethodPost, "/api/fm/delete", map[string]any{"items": []any{map[string]any{"path": h.p("sub")}}})
	if code != http.StatusBadRequest || out["error"] != errConfirm {
		t.Errorf("delete without confirm = %d %v", code, out)
	}
	if _, err := os.Stat(h.p("sub/in.txt")); err != nil {
		t.Fatal("folder was deleted despite the missing confirmation")
	}
	code, out = h.call(http.MethodPost, "/api/fm/delete", map[string]any{"items": []any{map[string]any{"path": h.p("sub"), "confirm": "sub"}}})
	if code != http.StatusOK {
		t.Fatalf("delete = %d %v", code, out)
	}
	id := out["trashed"].([]any)[0].(map[string]any)["id"].(string)

	// Trash view, then restore.
	code, out = h.call(http.MethodGet, "/api/fm/trash", nil)
	if code != http.StatusOK || len(out["items"].([]any)) != 1 {
		t.Fatalf("trash = %d %v", code, out)
	}
	if code, out = h.call(http.MethodPost, "/api/fm/trash/"+id+"/restore", nil); code != http.StatusOK {
		t.Fatalf("restore = %d %v", code, out)
	}
	if _, err := os.Stat(h.p("sub/in.txt")); err != nil {
		t.Errorf("restored folder lacks its content: %v", err)
	}
	if code, out = h.call(http.MethodPost, "/api/fm/trash/"+id+"/restore", nil); code != http.StatusNotFound {
		t.Errorf("second restore = %d %v", code, out)
	}

	// Recursive chmod is refused until confirmed, then runs as a job.
	code, out = h.call(http.MethodPost, "/api/fm/chmod", map[string]any{"path": h.p("sub"), "mode": "700", "recursive": true})
	if code != http.StatusBadRequest || out["error"] != errConfirm {
		t.Errorf("recursive chmod without confirm = %d %v", code, out)
	}
	code, out = h.call(http.MethodPost, "/api/fm/chmod", map[string]any{"path": h.p("sub"), "mode": "700", "recursive": true, "confirm": "sub"})
	if code != http.StatusAccepted {
		t.Fatalf("recursive chmod = %d %v", code, out)
	}
	if job := h.waitJob(out); job.State != jobs.StateSucceeded {
		t.Errorf("chmod job = %s %s", job.State, job.Err)
	}
	if st, _ := os.Stat(h.p("sub/in.txt")); st.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
	if code, out = h.call(http.MethodPost, "/api/fm/chmod", map[string]any{"path": h.p("sub"), "mode": "999"}); code != http.StatusBadRequest {
		t.Errorf("bad mode = %d %v", code, out)
	}
}

func TestFMAuditRecordsPaths(t *testing.T) {
	h := newFMHarness(t)
	h.call(http.MethodPost, "/api/fm/mkdir", map[string]any{"path": h.p("audited")})
	var found bool
	for _, e := range h.audit.entries {
		if e.Action == "POST /api/fm/mkdir" {
			found = true
			if e.Target != h.p("audited") {
				t.Errorf("audit target = %q, want the created path", e.Target)
			}
		}
	}
	if !found {
		t.Error("no audit entry for mkdir")
	}
}

func TestFMEditorConflictAndRedirect(t *testing.T) {
	h := newFMHarness(t)
	file := h.p("c.conf")
	_ = os.WriteFile(file, []byte("one\n"), 0o644)
	code, out := h.call(http.MethodGet, "/api/fm/file?path="+url.QueryEscape(file), nil)
	if code != http.StatusOK || out["content"] != "one\n" || out["language"] != "properties" {
		t.Fatalf("open = %d %v", code, out)
	}
	base := map[string]any{"size": out["size"], "mtime": out["mtime"], "hash": out["hash"]}
	_ = os.WriteFile(file, []byte("two\n"), 0o644)
	code, out = h.call(http.MethodPut, "/api/fm/file", map[string]any{"path": file, "content": "mine\n", "base": base})
	if code != http.StatusConflict || out["error"] != errEditConflict {
		t.Fatalf("stale save = %d %v", code, out)
	}
	if cur := out["current"].(map[string]any); cur["content"] != "two\n" {
		t.Errorf("conflict body = %v", cur)
	}
	code, out = h.call(http.MethodPut, "/api/fm/file", map[string]any{"path": file, "content": "mine\n"})
	if code != http.StatusOK {
		t.Errorf("unconditional save = %d %v", code, out)
	}

	_ = os.WriteFile(h.p("bin"), []byte("a\x00b"), 0o644)
	if code, out = h.call(http.MethodGet, "/api/fm/file?path="+url.QueryEscape(h.p("bin")), nil); code != http.StatusUnsupportedMediaType {
		t.Errorf("binary = %d %v", code, out)
	}
}

func (h *fmHarness) chunk(id string, offset int, data []byte, sum string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/fm/uploads/"+id+"?offset="+strconv.Itoa(offset), bytes.NewReader(data))
	req.RemoteAddr = "203.0.113.9:54321"
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: h.s.cookie})
	req.Header.Set(csrfHeader, h.s.csrf)
	req.Header.Set("Content-Type", "application/octet-stream")
	if sum != "" {
		req.Header.Set("X-Chunk-SHA256", sum)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func shaHex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func (h *fmHarness) get(path, rangeHdr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "203.0.113.9:54321"
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: h.s.cookie})
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func TestFMUploadDownloadRange(t *testing.T) {
	h := newFMHarness(t)
	data := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	code, out := h.call(http.MethodPost, "/api/fm/uploads", map[string]any{"dir": h.work, "name": "up.bin", "size": len(data)})
	if code != http.StatusCreated {
		t.Fatalf("start = %d %v", code, out)
	}
	id := out["id"].(string)

	first := data[:20]
	if rec := h.chunk(id, 0, first, shaHex(first)); rec.Code != http.StatusOK {
		t.Fatalf("chunk 1 = %d %s", rec.Code, rec.Body)
	}
	// A corrupt chunk is a 400 and the offset does not move.
	if rec := h.chunk(id, 20, data[20:], shaHex(first)); rec.Code != http.StatusBadRequest {
		t.Errorf("corrupt chunk = %d %s", rec.Code, rec.Body)
	}
	// Resume: the status says where to continue; a wrong offset says it too.
	code, out = h.call(http.MethodGet, "/api/fm/uploads/"+id, nil)
	if code != http.StatusOK || out["received"].(float64) != 20 {
		t.Fatalf("status = %d %v", code, out)
	}
	rec := h.chunk(id, 5, data[5:], shaHex(data[5:]))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"received":20`) {
		t.Errorf("wrong offset = %d %s", rec.Code, rec.Body)
	}
	if rec := h.chunk(id, 20, data[20:], shaHex(data[20:])); rec.Code != http.StatusOK {
		t.Fatalf("chunk 2 = %d %s", rec.Code, rec.Body)
	}
	code, out = h.call(http.MethodPost, "/api/fm/uploads/"+id+"/complete", map[string]any{"sha256": shaHex(data)})
	if code != http.StatusOK || out["path"] != h.p("up.bin") {
		t.Fatalf("complete = %d %v", code, out)
	}

	dl := "/api/fm/download?path=" + url.QueryEscape(h.p("up.bin"))
	full := h.get(dl, "")
	if full.Code != http.StatusOK || !bytes.Equal(full.Body.Bytes(), data) {
		t.Fatalf("download = %d %q", full.Code, full.Body)
	}
	if cd := full.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if ct := full.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	part := h.get(dl, "bytes=10-19")
	if part.Code != http.StatusPartialContent || part.Body.String() != "abcdefghij" {
		t.Errorf("range = %d %q", part.Code, part.Body)
	}
	if part.Header().Get("Content-Range") != "bytes 10-19/36" {
		t.Errorf("Content-Range = %q", part.Header().Get("Content-Range"))
	}
}

func TestFMFolderDownloadAndArchives(t *testing.T) {
	h := newFMHarness(t)
	_ = os.MkdirAll(h.p("proj/d"), 0o755)
	_ = os.WriteFile(h.p("proj/a.txt"), []byte("A"), 0o644)
	_ = os.WriteFile(h.p("proj/d/b.txt"), []byte("B"), 0o644)

	rec := h.get("/api/fm/download?format=zip&path="+url.QueryEscape(h.p("proj")), "")
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil || rec.Code != http.StatusOK {
		t.Fatalf("folder zip = %d %v", rec.Code, err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["proj/a.txt"] || !names["proj/d/b.txt"] {
		t.Errorf("zip entries = %v", names)
	}

	code, out := h.call(http.MethodPost, "/api/fm/archive", map[string]any{"dir": h.work, "names": []string{"proj"}, "dest": h.p("proj.tar.gz")})
	if code != http.StatusAccepted {
		t.Fatalf("archive = %d %v", code, out)
	}
	if job := h.waitJob(out); job.State != jobs.StateSucceeded {
		t.Fatalf("archive job = %s %s", job.State, job.Err)
	}
	_ = os.MkdirAll(h.p("out"), 0o755)
	code, out = h.call(http.MethodPost, "/api/fm/extract", map[string]any{"path": h.p("proj.tar.gz"), "dest": h.p("out")})
	if code != http.StatusAccepted {
		t.Fatalf("extract = %d %v", code, out)
	}
	if job := h.waitJob(out); job.State != jobs.StateSucceeded {
		t.Fatalf("extract job = %s %s", job.State, job.Err)
	}
	if b, _ := os.ReadFile(h.p("out/proj/d/b.txt")); string(b) != "B" {
		t.Errorf("extracted content = %q", b)
	}

	// A hostile archive fails its job and writes nothing.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../evil.txt")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	_ = os.WriteFile(h.p("evil.zip"), buf.Bytes(), 0o644)
	code, out = h.call(http.MethodPost, "/api/fm/extract", map[string]any{"path": h.p("evil.zip"), "dest": h.p("out")})
	if code != http.StatusAccepted {
		t.Fatalf("extract evil = %d %v", code, out)
	}
	if job := h.waitJob(out); job.State != jobs.StateFailed {
		t.Errorf("hostile extract job = %s, want failed", job.State)
	}
	if _, err := os.Stat(h.p("evil.txt")); err == nil {
		t.Error("hostile archive wrote outside the target")
	}
}
