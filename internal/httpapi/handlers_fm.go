package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/user"
	"path"
	"strconv"
	"time"

	"github.com/portbyte-id/server-panel/internal/fm"
	"github.com/portbyte-id/server-panel/internal/jobs"
)

// FileManager is the part of fm.Service the routes use. *fm.Service
// implements it; tests substitute a fake. Paths passed to it are already the
// output of Clean.
type FileManager interface {
	Clean(p string) (string, error)
	SameDevice(a, b string) bool

	List(clean string, opt fm.ListOptions) (fm.Listing, error)
	Stat(clean string) (fm.Entry, error)
	Disks() ([]fm.Mount, error)
	Mkdir(clean string, mode uint32) error
	Touch(clean string, mode uint32) error
	Rename(clean, newName string) error
	Move(ctx context.Context, src, dstDir string, progress fm.Progress) error
	Copy(ctx context.Context, src, dstDir string, progress fm.Progress) error
	CheckRecursive(clean, confirm string) error
	Chmod(ctx context.Context, clean string, mode uint32, recursive bool, confirm string, progress fm.Progress) (int, error)
	Chown(ctx context.Context, clean string, uid, gid int, recursive bool, confirm string, progress fm.Progress) (int, error)

	Trash(ctx context.Context, clean, confirm string, progress fm.Progress) (fm.TrashItem, error)
	PreflightTrash(clean, confirm string) error
	TrashNeedsJob(clean string) bool
	TrashList(ctx context.Context) ([]fm.TrashItem, error)
	Restore(ctx context.Context, id, newName string, progress fm.Progress) (string, error)
	RestoreNeedsJob(ctx context.Context, id string) bool
	DeletePermanently(ctx context.Context, id, confirm string, progress fm.Progress) error

	OpenFile(ctx context.Context, clean string) (fm.File, error)
	SaveFile(ctx context.Context, clean, content string, base *fm.Base) (fm.File, error)

	UploadStart(ctx context.Context, dir, name string, size int64, overwrite bool) (fm.UploadInfo, error)
	UploadStatus(ctx context.Context, id string) (fm.UploadInfo, error)
	UploadChunk(ctx context.Context, id string, offset int64, sum string, body io.Reader) (fm.UploadInfo, error)
	UploadComplete(ctx context.Context, id, wholeSum string) (string, error)
	UploadCancel(ctx context.Context, id string) error

	OpenDownload(clean string) (*os.File, os.FileInfo, error)
	StreamFolder(ctx context.Context, w io.Writer, clean, format string) error
	CreateArchive(ctx context.Context, dir string, names []string, dest, format string, progress fm.Progress) error
	Extract(ctx context.Context, archive, dest string, overwrite bool, progress fm.Progress) error
}

// File-manager error classes.
const (
	errProtected    = "protected_path"
	errConfirm      = "confirmation_required"
	errTooLarge     = "too_large"
	errBinary       = "binary_file"
	errChecksum     = "checksum_mismatch"
	errNoSpace      = "no_space"
	errUnsafe       = "unsafe_archive"
	errEditConflict = "edit_conflict"
	errUseConfig    = "use_config_editor"
	errOffset       = "offset_mismatch"
)

const (
	fmChunkBodyLimit  = 256 << 20 // hard stop above any configurable chunk size
	fmChunkTimeout    = 3 * time.Minute
	fmEditorBodyLimit = 320 << 20
)

func (s *server) fmRoutes(mux *http.ServeMux) {
	h := func(fn func(FileManager, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Files == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"the file manager is not available on this host; see the daemon log")
				return
			}
			fn(s.deps.Files, w, r)
		}))
	}
	mux.Handle("GET /api/fm/list", h(s.fmList))
	mux.Handle("GET /api/fm/stat", h(s.fmStat))
	mux.Handle("GET /api/fm/disks", h(s.fmDisks))
	mux.Handle("POST /api/fm/mkdir", h(s.fmMkdir))
	mux.Handle("POST /api/fm/create", h(s.fmCreate))
	mux.Handle("POST /api/fm/rename", h(s.fmRename))
	mux.Handle("POST /api/fm/move", h(s.fmMove))
	mux.Handle("POST /api/fm/copy", h(s.fmCopy))
	mux.Handle("POST /api/fm/chmod", h(s.fmChmod))
	mux.Handle("POST /api/fm/chown", h(s.fmChown))
	mux.Handle("POST /api/fm/delete", h(s.fmDelete))

	mux.Handle("GET /api/fm/trash", h(s.fmTrashList))
	mux.Handle("POST /api/fm/trash/{id}/restore", h(s.fmTrashRestore))
	mux.Handle("DELETE /api/fm/trash/{id}", h(s.fmTrashDelete))

	mux.Handle("GET /api/fm/file", h(s.fmGetFile))
	mux.Handle("PUT /api/fm/file", h(s.fmPutFile))

	mux.Handle("POST /api/fm/uploads", h(s.fmUploadStart))
	mux.Handle("GET /api/fm/uploads/{id}", h(s.fmUploadStatus))
	mux.Handle("PUT /api/fm/uploads/{id}", h(s.fmUploadChunk))
	mux.Handle("POST /api/fm/uploads/{id}/complete", h(s.fmUploadComplete))
	mux.Handle("DELETE /api/fm/uploads/{id}", h(s.fmUploadCancel))

	mux.Handle("GET /api/fm/download", h(s.fmDownload))
	mux.Handle("POST /api/fm/archive", h(s.fmArchive))
	mux.Handle("POST /api/fm/extract", h(s.fmExtract))
}

// writeFMError maps a file-manager error to a status and a class.
func (s *server) writeFMError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		conflict *fm.ConflictError
		redirect *fm.RedirectError
		offset   *fm.OffsetError
	)
	body := func(status int, class string, extra map[string]any) {
		m := map[string]any{"error": class, "detail": err.Error()}
		for k, v := range extra {
			m[k] = v
		}
		writeJSON(w, status, m)
	}
	switch {
	case errors.As(err, &conflict):
		body(http.StatusConflict, errEditConflict, map[string]any{"current": conflict.Current})
	case errors.As(err, &redirect):
		body(http.StatusConflict, errUseConfig, map[string]any{"path": redirect.Path, "tree": redirect.Tree})
	case errors.As(err, &offset):
		body(http.StatusConflict, errOffset, map[string]any{"received": offset.Received})
	case errors.Is(err, fm.ErrInvalidPath), errors.Is(err, fm.ErrNotDir), errors.Is(err, fm.ErrIsDir),
		errors.Is(err, fm.ErrSymlink), errors.Is(err, fm.ErrIntoItself), errors.Is(err, fm.ErrCrossMount),
		errors.Is(err, fm.ErrUnsupported):
		body(http.StatusBadRequest, errInvalidInput, nil)
	case errors.Is(err, fm.ErrProtected), errors.Is(err, os.ErrPermission):
		body(http.StatusForbidden, errProtected, nil)
	case errors.Is(err, fm.ErrConfirm):
		body(http.StatusBadRequest, errConfirm, nil)
	case errors.Is(err, fm.ErrNotFound):
		body(http.StatusNotFound, errNotFound, nil)
	case errors.Is(err, fm.ErrExists):
		body(http.StatusConflict, errConflict, nil)
	case errors.Is(err, fm.ErrTooLarge):
		body(http.StatusRequestEntityTooLarge, errTooLarge, nil)
	case errors.Is(err, fm.ErrBinary):
		body(http.StatusUnsupportedMediaType, errBinary, nil)
	case errors.Is(err, fm.ErrChecksum):
		body(http.StatusBadRequest, errChecksum, nil)
	case errors.Is(err, fm.ErrNoSpace):
		body(http.StatusInsufficientStorage, errNoSpace, nil)
	case errors.Is(err, fm.ErrArchive):
		body(http.StatusUnprocessableEntity, errUnsafe, nil)
	default:
		s.writeInternal(w, r, err)
	}
}

// fmPath cleans a path from the query or a body field, answering 400 itself.
func (s *server) fmPath(f FileManager, w http.ResponseWriter, r *http.Request, raw string) (string, bool) {
	clean, err := f.Clean(raw)
	if err != nil {
		s.writeFMError(w, r, err)
		return "", false
	}
	return clean, true
}

func (s *server) fmBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := decodeJSON(w, r, v); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return false
	}
	return true
}

// fmJob starts fn as a job that outlives the request and answers 202.
func (s *server) fmJob(w http.ResponseWriter, r *http.Request, kind string, fn func(ctx context.Context, p fm.Progress) error) {
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), kind, func(ctx context.Context, out *jobs.Writer) error {
		return fn(ctx, func(line string) { _ = out.Line(line) })
	})
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Job jobs.Job `json:"job"`
	}{job})
}

func (s *server) fmList(f FileManager, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	raw := q.Get("path")
	if raw == "" {
		raw = "/"
	}
	clean, ok := s.fmPath(f, w, r, raw)
	if !ok {
		return
	}
	opt := fm.ListOptions{Hidden: q.Get("hidden") == "1", Sort: q.Get("sort"), Desc: q.Get("desc") == "1"}
	opt.Offset, _ = strconv.Atoi(q.Get("offset"))
	opt.Limit, _ = strconv.Atoi(q.Get("limit"))
	l, err := f.List(clean, opt)
	if err != nil {
		s.writeFMError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *server) fmStat(f FileManager, w http.ResponseWriter, r *http.Request) {
	clean, ok := s.fmPath(f, w, r, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	e, err := f.Stat(clean)
	if err != nil {
		s.writeFMError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *server) fmDisks(f FileManager, w http.ResponseWriter, r *http.Request) {
	m, err := f.Disks()
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Mounts []fm.Mount `json:"mounts"`
	}{m})
}

type pathBody struct {
	Path string `json:"path"`
}

func (s *server) fmMkdir(f FileManager, w http.ResponseWriter, r *http.Request) {
	s.fmCreateKind(f, w, r, f.Mkdir, 0o755)
}

func (s *server) fmCreate(f FileManager, w http.ResponseWriter, r *http.Request) {
	s.fmCreateKind(f, w, r, f.Touch, 0o644)
}

func (s *server) fmCreateKind(f FileManager, w http.ResponseWriter, r *http.Request, op func(string, uint32) error, mode uint32) {
	var body pathBody
	if !s.fmBody(w, r, &body) {
		return
	}
	clean, ok := s.fmPath(f, w, r, body.Path)
	if !ok {
		return
	}
	noteTarget(r, clean)
	if err := op(clean, mode); err != nil {
		s.writeFMError(w, r, err)
		return
	}
	e, err := f.Stat(clean)
	if err != nil {
		s.writeFMError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *server) fmRename(f FileManager, w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !s.fmBody(w, r, &body) {
		return
	}
	clean, ok := s.fmPath(f, w, r, body.Path)
	if !ok {
		return
	}
	noteTarget(r, clean)
	note(r, "to", body.Name)
	if err := f.Rename(clean, body.Name); err != nil {
		s.writeFMError(w, r, err)
		return
	}
	e, err := f.Stat(path.Join(path.Dir(clean), body.Name))
	if err != nil {
		s.writeFMError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// fmTransfer reads a move or copy request: every source cleaned, and the
// destination cleaned.
func (s *server) fmTransfer(f FileManager, w http.ResponseWriter, r *http.Request) (srcs []string, dest string, ok bool) {
	var body struct {
		Paths []string `json:"paths"`
		Dest  string   `json:"dest"`
	}
	if !s.fmBody(w, r, &body) {
		return nil, "", false
	}
	if len(body.Paths) == 0 {
		writeError(w, http.StatusBadRequest, errInvalidInput, "no paths given")
		return nil, "", false
	}
	if dest, ok = s.fmPath(f, w, r, body.Dest); !ok {
		return nil, "", false
	}
	for _, p := range body.Paths {
		c, ok := s.fmPath(f, w, r, p)
		if !ok {
			return nil, "", false
		}
		srcs = append(srcs, c)
	}
	note(r, "paths", srcs)
	noteTarget(r, dest)
	return srcs, dest, true
}

func (s *server) fmCopy(f FileManager, w http.ResponseWriter, r *http.Request) {
	srcs, dest, ok := s.fmTransfer(f, w, r)
	if !ok {
		return
	}
	s.fmJob(w, r, "fm.copy", func(ctx context.Context, p fm.Progress) error {
		for _, src := range srcs {
			p(fmt.Sprintf("copying %s to %s", src, dest))
			if err := f.Copy(ctx, src, dest, p); err != nil {
				return fmt.Errorf("%s: %w", src, err)
			}
		}
		return nil
	})
}

func (s *server) fmMove(f FileManager, w http.ResponseWriter, r *http.Request) {
	srcs, dest, ok := s.fmTransfer(f, w, r)
	if !ok {
		return
	}
	run := func(ctx context.Context, p fm.Progress) error {
		for _, src := range srcs {
			if err := f.Move(ctx, src, dest, p); err != nil {
				return fmt.Errorf("%s: %w", src, err)
			}
		}
		return nil
	}
	for _, src := range srcs {
		if !f.SameDevice(path.Dir(src), dest) {
			s.fmJob(w, r, "fm.move", run)
			return
		}
	}
	if err := run(r.Context(), nil); err != nil {
		s.writeFMError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Moved int `json:"moved"`
	}{len(srcs)})
}

// lookupID resolves an owner or group given as a name or a number. Empty is -1,
// which leaves that half unchanged.
func lookupID(raw string, group bool) (int, error) {
	if raw == "" {
		return -1, nil
	}
	if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
		return n, nil
	}
	if group {
		g, err := user.LookupGroup(raw)
		if err != nil {
			return 0, fmt.Errorf("no group named %q", raw)
		}
		return strconv.Atoi(g.Gid)
	}
	u, err := user.Lookup(raw)
	if err != nil {
		return 0, fmt.Errorf("no user named %q", raw)
	}
	return strconv.Atoi(u.Uid)
}

type changedResponse struct {
	Changed int `json:"changed"`
}

func (s *server) fmChmod(f FileManager, w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path      string `json:"path"`
		Mode      string `json:"mode"`
		Recursive bool   `json:"recursive"`
		Confirm   string `json:"confirm"`
	}
	if !s.fmBody(w, r, &body) {
		return
	}
	clean, ok := s.fmPath(f, w, r, body.Path)
	if !ok {
		return
	}
	mode, err := strconv.ParseUint(body.Mode, 8, 32)
	if err != nil || len(body.Mode) < 3 || len(body.Mode) > 4 {
		writeError(w, http.StatusBadRequest, errInvalidInput, "mode must be three or four octal digits, for example 755")
		return
	}
	noteTarget(r, clean)
	note(r, "mode", body.Mode)
	note(r, "recursive", body.Recursive)
	if !body.Recursive {
		n, err := f.Chmod(r.Context(), clean, uint32(mode), false, "", nil)
		if err != nil {
			s.writeFMError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, changedResponse{n})
		return
	}
	// Refuse before a job exists: a bad confirmation is an answer, not a failed job.
	if err := f.CheckRecursive(clean, body.Confirm); err != nil {
		s.writeFMError(w, r, err)
		return
	}
	s.fmJob(w, r, "fm.chmod", func(ctx context.Context, p fm.Progress) error {
		n, err := f.Chmod(ctx, clean, uint32(mode), true, body.Confirm, p)
		p(fmt.Sprintf("%d entries changed", n))
		return err
	})
}

func (s *server) fmChown(f FileManager, w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path      string `json:"path"`
		Owner     string `json:"owner"`
		Group     string `json:"group"`
		Recursive bool   `json:"recursive"`
		Confirm   string `json:"confirm"`
	}
	if !s.fmBody(w, r, &body) {
		return
	}
	clean, ok := s.fmPath(f, w, r, body.Path)
	if !ok {
		return
	}
	uid, err := lookupID(body.Owner, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput, err.Error())
		return
	}
	gid, err := lookupID(body.Group, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput, err.Error())
		return
	}
	noteTarget(r, clean)
	note(r, "owner", body.Owner)
	note(r, "group", body.Group)
	note(r, "recursive", body.Recursive)
	if !body.Recursive {
		n, err := f.Chown(r.Context(), clean, uid, gid, false, "", nil)
		if err != nil {
			s.writeFMError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, changedResponse{n})
		return
	}
	if err := f.CheckRecursive(clean, body.Confirm); err != nil {
		s.writeFMError(w, r, err)
		return
	}
	s.fmJob(w, r, "fm.chown", func(ctx context.Context, p fm.Progress) error {
		n, err := f.Chown(ctx, clean, uid, gid, true, body.Confirm, p)
		p(fmt.Sprintf("%d entries changed", n))
		return err
	})
}

type deleteItem struct {
	Path    string `json:"path"`
	Confirm string `json:"confirm"`
}

func (s *server) fmDelete(f FileManager, w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []deleteItem `json:"items"`
	}
	if !s.fmBody(w, r, &body) {
		return
	}
	if len(body.Items) == 0 {
		writeError(w, http.StatusBadRequest, errInvalidInput, "nothing to delete")
		return
	}
	items := make([]deleteItem, len(body.Items))
	paths := make([]string, len(body.Items))
	needsJob := false
	for i, it := range body.Items {
		c, ok := s.fmPath(f, w, r, it.Path)
		if !ok {
			return
		}
		// Refuse a protected or unconfirmed item before anything moves or a job exists.
		if err := f.PreflightTrash(c, it.Confirm); err != nil {
			s.writeFMError(w, r, fmt.Errorf("%s: %w", c, err))
			return
		}
		items[i] = deleteItem{Path: c, Confirm: it.Confirm}
		paths[i] = c
		needsJob = needsJob || f.TrashNeedsJob(c)
	}
	note(r, "paths", paths)
	noteTarget(r, paths[0])
	run := func(ctx context.Context, p fm.Progress) ([]fm.TrashItem, error) {
		var done []fm.TrashItem
		for _, it := range items {
			t, err := f.Trash(ctx, it.Path, it.Confirm, p)
			if err != nil {
				return done, fmt.Errorf("%s: %w", it.Path, err)
			}
			done = append(done, t)
		}
		return done, nil
	}
	if needsJob {
		s.fmJob(w, r, "fm.delete", func(ctx context.Context, p fm.Progress) error {
			_, err := run(ctx, p)
			return err
		})
		return
	}
	done, err := run(r.Context(), nil)
	if err != nil {
		// Items before the failing one are already in the bin; say how many.
		note(r, "deleted", len(done))
		s.writeFMError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Trashed []fm.TrashItem `json:"trashed"`
	}{done})
}

func (s *server) fmTrashList(f FileManager, w http.ResponseWriter, r *http.Request) {
	items, err := f.TrashList(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []fm.TrashItem `json:"items"`
	}{items})
}

func (s *server) fmTrashRestore(f FileManager, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name string `json:"name"`
	}
	if r.ContentLength != 0 && !s.fmBody(w, r, &body) {
		return
	}
	noteTarget(r, id)
	note(r, "name", body.Name)
	if f.RestoreNeedsJob(r.Context(), id) {
		s.fmJob(w, r, "fm.restore", func(ctx context.Context, p fm.Progress) error {
			_, err := f.Restore(ctx, id, body.Name, p)
			return err
		})
		return
	}
	to, err := f.Restore(r.Context(), id, body.Name, nil)
	if err != nil {
		s.writeFMError(w, r, err)
		return
	}
	note(r, "restored_to", to)
	writeJSON(w, http.StatusOK, struct {
		Path string `json:"path"`
	}{to})
}

func (s *server) fmTrashDelete(f FileManager, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	noteTarget(r, id)
	if err := f.DeletePermanently(r.Context(), id, r.URL.Query().Get("confirm"), nil); err != nil {
		s.writeFMError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) fmGetFile(f FileManager, w http.ResponseWriter, r *http.Request) {
	clean, ok := s.fmPath(f, w, r, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	file, err := f.OpenFile(r.Context(), clean)
	if err != nil {
		s.writeFMError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		fm.File
		Language string `json:"language"`
	}{file, fm.LanguageOf(path.Base(clean))})
}

func (s *server) fmPutFile(f FileManager, w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string   `json:"path"`
		Content string   `json:"content"`
		Base    *fm.Base `json:"base"`
	}
	// The editor's own size limit (a setting) is enforced by the service; this
	// cap only bounds what is read before that check can run.
	if err := decodeJSONLimit(w, r, &body, fmEditorBodyLimit); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	clean, ok := s.fmPath(f, w, r, body.Path)
	if !ok {
		return
	}
	noteTarget(r, clean)
	saved, err := f.SaveFile(r.Context(), clean, body.Content, body.Base)
	if err != nil {
		s.writeFMError(w, r, err)
		return
	}
	note(r, "size", saved.Size)
	writeJSON(w, http.StatusOK, saved)
}

func (s *server) fmUploadStart(f FileManager, w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dir       string `json:"dir"`
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		Overwrite bool   `json:"overwrite"`
	}
	if !s.fmBody(w, r, &body) {
		return
	}
	u, err := f.UploadStart(r.Context(), body.Dir, body.Name, body.Size, body.Overwrite)
	if err != nil {
		s.writeFMError(w, r, err)
		return
	}
	noteTarget(r, path.Join(u.Dir, u.Name))
	note(r, "upload_id", u.ID)
	note(r, "size", u.Size)
	writeJSON(w, http.StatusCreated, u)
}

func (s *server) fmUploadStatus(f FileManager, w http.ResponseWriter, r *http.Request) {
	u, err := f.UploadStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeFMError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *server) fmUploadChunk(f FileManager, w http.ResponseWriter, r *http.Request) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, errInvalidInput, "offset must be a non-negative whole number")
		return
	}
	// The server-wide read timeout is 30s, too short for a chunk over a slow
	// link; a chunk has its own, bounded one.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(fmChunkTimeout))
	r.Body = http.MaxBytesReader(w, r.Body, fmChunkBodyLimit)
	u, err := f.UploadChunk(r.Context(), r.PathValue("id"), offset, r.Header.Get("X-Chunk-SHA256"), r.Body)
	if err != nil {
		s.writeFMError(w, r, err)
		return
	}
	note(r, "upload_id", u.ID)
	note(r, "offset", offset)
	writeJSON(w, http.StatusOK, u)
}

func (s *server) fmUploadComplete(f FileManager, w http.ResponseWriter, r *http.Request) {
	var body struct {
		SHA256 string `json:"sha256"`
	}
	if r.ContentLength != 0 && !s.fmBody(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	to, err := f.UploadComplete(r.Context(), id, body.SHA256)
	if err != nil {
		s.writeFMError(w, r, err)
		return
	}
	noteTarget(r, to)
	note(r, "upload_id", id)
	writeJSON(w, http.StatusOK, struct {
		Path string `json:"path"`
	}{to})
}

func (s *server) fmUploadCancel(f FileManager, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	noteTarget(r, id)
	if err := f.UploadCancel(r.Context(), id); err != nil {
		s.writeFMError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// contentDisposition builds an attachment header that survives any file name:
// mime handles quoting and non-ASCII names, and a name it cannot encode falls
// back to a fixed one rather than an unquoted header.
func contentDisposition(name string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": name}); v != "" {
		return v
	}
	return `attachment; filename="download"`
}

func (s *server) fmDownload(f FileManager, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clean, ok := s.fmPath(f, w, r, q.Get("path"))
	if !ok {
		return
	}
	file, info, err := f.OpenDownload(clean)
	if err == nil {
		defer func() { _ = file.Close() }()
		// Always an attachment with an opaque type: a stored HTML or SVG file
		// must download, never render in the panel's origin.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", contentDisposition(path.Base(clean)))
		http.ServeContent(w, r, path.Base(clean), info.ModTime(), file)
		return
	}
	if !errors.Is(err, fm.ErrIsDir) {
		s.writeFMError(w, r, err)
		return
	}

	format := q.Get("format")
	if format == "" {
		format = fm.FormatTarGz
	}
	var ctype, ext string
	switch format {
	case fm.FormatZip:
		ctype, ext = "application/zip", ".zip"
	case fm.FormatTarGz:
		ctype, ext = "application/gzip", ".tar.gz"
	default:
		writeError(w, http.StatusBadRequest, errInvalidInput, "format must be zip or tar.gz")
		return
	}
	name := path.Base(clean)
	if clean == "/" {
		name = "root"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", contentDisposition(name+ext))
	// The status is sent with the first byte, so an error after that point
	// can only cut the stream; the client sees a truncated archive.
	if err := f.StreamFolder(r.Context(), w, clean, format); err != nil {
		s.logger.WarnContext(r.Context(), "folder download failed", "path", clean, "error", err)
	}
}

func (s *server) fmArchive(f FileManager, w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dir    string   `json:"dir"`
		Names  []string `json:"names"`
		Dest   string   `json:"dest"`
		Format string   `json:"format"`
	}
	if !s.fmBody(w, r, &body) {
		return
	}
	dir, ok := s.fmPath(f, w, r, body.Dir)
	if !ok {
		return
	}
	dest, ok := s.fmPath(f, w, r, body.Dest)
	if !ok {
		return
	}
	if body.Format == "" {
		body.Format = fm.ArchiveFormatOf(dest)
	}
	noteTarget(r, dest)
	note(r, "names", body.Names)
	s.fmJob(w, r, "fm.archive", func(ctx context.Context, p fm.Progress) error {
		p(fmt.Sprintf("creating %s", dest))
		return f.CreateArchive(ctx, dir, body.Names, dest, body.Format, p)
	})
}

func (s *server) fmExtract(f FileManager, w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path      string `json:"path"`
		Dest      string `json:"dest"`
		Overwrite bool   `json:"overwrite"`
	}
	if !s.fmBody(w, r, &body) {
		return
	}
	archive, ok := s.fmPath(f, w, r, body.Path)
	if !ok {
		return
	}
	dest, ok := s.fmPath(f, w, r, body.Dest)
	if !ok {
		return
	}
	noteTarget(r, dest)
	note(r, "archive", archive)
	s.fmJob(w, r, "fm.extract", func(ctx context.Context, p fm.Progress) error {
		return f.Extract(ctx, archive, dest, body.Overwrite, p)
	})
}
