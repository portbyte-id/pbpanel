package fm

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// Sentinel errors beyond the policy's. The API maps each to a status code.
var (
	ErrNotFound    = errors.New("no such file or directory")
	ErrExists      = errors.New("already exists")
	ErrNotDir      = errors.New("not a directory")
	ErrIsDir       = errors.New("is a directory")
	ErrSymlink     = errors.New("refusing to follow a symbolic link")
	ErrCrossMount  = errors.New("refusing to cross a mount point")
	ErrBinary      = errors.New("binary file")
	ErrTooLarge    = errors.New("file is too large")
	ErrChecksum    = errors.New("checksum mismatch")
	ErrNoSpace     = errors.New("not enough free disk space")
	ErrUnsupported = errors.New("not supported on this host")
	ErrArchive     = errors.New("unsafe or invalid archive")
	ErrIntoItself  = errors.New("cannot move or copy a folder into itself")
)

// ConflictError is a save whose base no longer matches the file on disk.
// Current is what is on disk now, so the client can show both versions.
type ConflictError struct {
	Current File
}

func (e *ConflictError) Error() string {
	return "the file changed on disk since it was opened"
}

// RedirectError reports a path that must be edited through the config editor,
// because that tree is owned by confedit (plan 3.6).
type RedirectError struct {
	Path string
	Tree string
}

func (e *RedirectError) Error() string {
	return fmt.Sprintf("%s is under %s, which the config editor manages", e.Path, e.Tree)
}

// OffsetError is a chunk sent at the wrong offset; Received is where the
// server is, which is where the client resumes.
type OffsetError struct {
	Received int64
}

func (e *OffsetError) Error() string {
	return fmt.Sprintf("chunk offset does not match; the server has %d bytes", e.Received)
}

// Limits are the panel settings the file manager reads on every operation, so
// a change on the Settings page applies without a restart.
type Limits struct {
	EditorMaxBytes     int64
	ChunkMaxBytes      int64
	TrashMaxBytes      int64
	TrashRetentionDays int
	UploadSweepAge     time.Duration
	ArchiveMaxBytes    int64
	ArchiveMaxEntries  int
}

// DefaultLimits are the registry defaults, used where no registry is wired.
func DefaultLimits() Limits {
	return Limits{
		EditorMaxBytes:     2 << 20,
		ChunkMaxBytes:      8 << 20,
		TrashMaxBytes:      10 << 30,
		TrashRetentionDays: 30,
		UploadSweepAge:     24 * time.Hour,
		ArchiveMaxBytes:    20 << 30,
		ArchiveMaxEntries:  200000,
	}
}

// Kind classifies a directory entry.
type Kind string

// Entry kinds.
const (
	KindFile    Kind = "file"
	KindDir     Kind = "dir"
	KindSymlink Kind = "symlink"
	KindOther   Kind = "other"
)

// Entry is one row of a listing, and the result of Stat.
type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Kind  Kind   `json:"kind"`
	Size  int64  `json:"size"`
	Mode  uint32 `json:"mode"` // permission and special bits, e.g. 0o755
	UID   uint32 `json:"uid"`
	GID   uint32 `json:"gid"`
	Owner string `json:"owner"`
	Group string `json:"group"`
	MTime int64  `json:"mtime"` // unix nanoseconds
	// Target is where a symlink points; TargetKind is what it points at, so
	// the UI can open a link to a folder as a folder. Empty for other kinds.
	Target     string `json:"target,omitempty"`
	TargetKind Kind   `json:"target_kind,omitempty"`
}

// ListOptions shape a listing.
type ListOptions struct {
	Hidden bool
	Sort   string // name (default), size, mtime
	Desc   bool
	Offset int
	Limit  int // 0 means DefaultListLimit
}

// DefaultListLimit and MaxListLimit bound one page of a listing.
const (
	DefaultListLimit = 500
	MaxListLimit     = 5000
)

// Listing is one page of a directory.
type Listing struct {
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
	Total   int     `json:"total"`
	Offset  int     `json:"offset"`
}

// Mount is one filesystem with its usage.
type Mount struct {
	Device string `json:"device"`
	Path   string `json:"path"`
	Type   string `json:"type"`
	Total  uint64 `json:"total"`
	Used   uint64 `json:"used"`
	Free   uint64 `json:"free"`
}

// File is a text file opened for editing. Hash is the SHA-256 of Content; with
// MTime and Size it is the base a save is checked against.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int64  `json:"size"`
	MTime   int64  `json:"mtime"`
	Hash    string `json:"hash"`
	Mode    uint32 `json:"mode"`
}

// Base is what the editor saw when it opened a file.
type Base struct {
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	Hash  string `json:"hash"`
}

// TrashItem is one entry of the recycle bin.
type TrashItem struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	OrigPath  string    `json:"orig_path"`
	Size      int64     `json:"size"`
	IsDir     bool      `json:"is_dir"`
	Mode      uint32    `json:"mode"`
	UID       uint32    `json:"uid"`
	GID       uint32    `json:"gid"`
	DeletedAt time.Time `json:"deleted_at"`
}

// UploadInfo is the state of a chunked upload.
type UploadInfo struct {
	ID        string `json:"id"`
	Dir       string `json:"dir"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Received  int64  `json:"received"`
	Overwrite bool   `json:"overwrite"`
	ChunkMax  int64  `json:"chunk_max"`
}

// Progress receives human-readable lines from long operations; it feeds a job.
type Progress func(line string)

func (p Progress) say(format string, args ...any) {
	if p != nil {
		p(fmt.Sprintf(format, args...))
	}
}

// LanguageOf names the CodeMirror language for a file name: CodeMirror picks a mode from the
// extension, and the server's answer keeps that rule in one place.
func LanguageOf(name string) string {
	switch ext := path.Ext(name); ext {
	case ".go":
		return "go"
	case ".js", ".mjs", ".cjs", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".json":
		return "json"
	case ".php", ".phtml":
		return "php"
	case ".py":
		return "python"
	case ".sh", ".bash", ".zsh":
		return "shell"
	case ".yml", ".yaml":
		return "yaml"
	case ".md", ".markdown":
		return "markdown"
	case ".html", ".htm":
		return "html"
	case ".css", ".scss":
		return "css"
	case ".xml", ".svg":
		return "xml"
	case ".sql":
		return "sql"
	case ".conf", ".ini", ".cfg", ".toml", ".env":
		return "properties"
	}
	if name == "Dockerfile" {
		return "shell"
	}
	return ""
}

// Archive formats.
const (
	FormatZip   = "zip"
	FormatTarGz = "tar.gz"
	FormatTar   = "tar"
)

// ArchiveFormatOf picks a format from a file name, or "".
func ArchiveFormatOf(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".zip"):
		return FormatZip
	case strings.HasSuffix(l, ".tar.gz"), strings.HasSuffix(l, ".tgz"):
		return FormatTarGz
	case strings.HasSuffix(l, ".tar"):
		return FormatTar
	}
	return ""
}
