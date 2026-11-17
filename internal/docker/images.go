package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Image is the subset of an image inspect the panel uses.
type Image struct {
	// ID is the content address (`sha256:...`). Two references that resolve to
	// the same ID are the same image.
	ID          string   `json:"Id"`
	RepoTags    []string `json:"RepoTags"`
	RepoDigests []string `json:"RepoDigests"`
	Size        int64    `json:"Size"`
}

// Progress receives human-readable progress lines from a pull or a build. It is
// what a job writes to its output; a nil Progress discards them.
type Progress func(line string)

func (p Progress) emit(line string) {
	if p != nil && line != "" {
		p(line)
	}
}

// ImageInspect returns the local image a reference resolves to, or an error
// matching ErrNotFound when it is not present.
func (c *Client) ImageInspect(ctx context.Context, ref string) (Image, error) {
	if err := checkRef(ref); err != nil {
		return Image{}, err
	}
	var img Image
	err := c.call(ctx, request{op: "inspect image " + ref, method: http.MethodGet, path: "/images/" + ref + "/json"}, &img)
	return img, err
}

// ImagePull pulls a reference and returns the image it resolved to.
//
// The Engine answers 200 and reports a failed pull as a line inside the stream;
// that line is turned into the returned error. The result is then inspected
// rather than inferred from the stream ending, so "the stream ended" is never
// mistaken for "the image is here".
func (c *Client) ImagePull(ctx context.Context, ref string, progress Progress) (Image, error) {
	if err := checkRef(ref); err != nil {
		return Image{}, err
	}
	q := url.Values{}
	name, tag := splitRef(ref)
	q.Set("fromImage", name)
	if tag != "" {
		q.Set("tag", tag)
	}
	resp, err := c.do(ctx, request{op: "pull " + ref, method: http.MethodPost, path: "/images/create", query: q})
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Layer progress arrives many times a second per layer. Only a change of
	// status per layer is passed on, so a pull is a few dozen job lines instead
	// of thousands that would push the useful ones out of the capped output.
	last := map[string]string{}
	err = readMessages(resp.Body, func(m message) {
		if m.Status == "" || last[m.ID] == m.Status {
			return
		}
		last[m.ID] = m.Status
		if m.ID != "" {
			progress.emit(m.ID + ": " + m.Status)
		} else {
			progress.emit(m.Status)
		}
	})
	if err != nil {
		return Image{}, fmt.Errorf("docker: pull %s: %w", ref, err)
	}
	img, err := c.ImageInspect(ctx, ref)
	if err != nil {
		return Image{}, fmt.Errorf("docker: pull %s finished but the image is not present: %w", ref, err)
	}
	return img, nil
}

// splitRef separates a tag from a name for the pull endpoint. A digest
// reference stays whole: the Engine reads `name@sha256:...` from fromImage.
func splitRef(ref string) (name, tag string) {
	if strings.Contains(ref, "@") {
		return ref, ""
	}
	// A colon after the last slash is a tag; one before it is a registry port.
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

// BuildOptions describes a build. Its context is either one Dockerfile given as
// bytes, or a directory on the host (ContextDir) whose whole tree is sent.
type BuildOptions struct {
	// Tag is the reference the built image gets, e.g. `panel/nginx:1.29-…`.
	Tag string
	// Dockerfile is the file's content. Used when ContextDir is empty; the
	// context is then this one file and nothing else.
	Dockerfile []byte
	// ContextDir is an absolute host directory whose regular files and
	// directories form the build context, Dockerfile included. Symbolic links
	// are refused: the directory is panel-written, and a link could pull a file
	// from outside it into the image.
	ContextDir string
	// Pull makes the Engine check the registry for a newer base image.
	Pull bool
	// Labels are set on the image.
	Labels map[string]string
}

// ImageBuild builds an image from a Dockerfile and returns it.
//
// As with a pull, a failed build is a 200 with an error line in the stream.
// The stream alone is not trusted either way (risk 11): the tag is inspected
// after the stream ends and its absence is a failure.
func (c *Client) ImageBuild(ctx context.Context, opts BuildOptions, progress Progress) (Image, error) {
	if err := checkRef(opts.Tag); err != nil {
		return Image{}, err
	}
	var buildContext *bytes.Reader
	var err error
	if opts.ContextDir != "" {
		buildContext, err = tarDir(opts.ContextDir)
	} else {
		if len(bytes.TrimSpace(opts.Dockerfile)) == 0 {
			return Image{}, errors.New("docker: build: Dockerfile is empty")
		}
		buildContext, err = tarOf("Dockerfile", opts.Dockerfile)
	}
	if err != nil {
		return Image{}, fmt.Errorf("docker: build %s: %w", opts.Tag, err)
	}

	q := url.Values{}
	q.Set("t", opts.Tag)
	q.Set("rm", "1")
	q.Set("forcerm", "1")
	// The classic builder: it needs no session and no client-side BuildKit
	// machinery, which is all a one-RUN Dockerfile calls for.
	q.Set("version", "1")
	if opts.Pull {
		q.Set("pull", "1")
	}
	if len(opts.Labels) > 0 {
		labels, err := json.Marshal(opts.Labels)
		if err != nil {
			return Image{}, fmt.Errorf("docker: build %s: %w", opts.Tag, err)
		}
		q.Set("labels", string(labels))
	}

	resp, err := c.do(ctx, request{
		op: "build " + opts.Tag, method: http.MethodPost, path: "/build",
		query: q, body: buildContext, contentType: "application/x-tar",
	})
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	err = readMessages(resp.Body, func(m message) {
		progress.emit(strings.TrimRight(m.Stream, "\n"))
		progress.emit(m.Status)
	})
	if err != nil {
		return Image{}, fmt.Errorf("docker: build %s: %w", opts.Tag, err)
	}
	img, err := c.ImageInspect(ctx, opts.Tag)
	if err != nil {
		return Image{}, fmt.Errorf("docker: build %s finished but the image is not present: %w", opts.Tag, err)
	}
	return img, nil
}

// ImageTag gives an existing image a second reference. The image itself is not
// copied: both references name the same content address.
func (c *Client) ImageTag(ctx context.Context, src, dst string) error {
	if err := checkRef(src); err != nil {
		return err
	}
	if err := checkRef(dst); err != nil {
		return err
	}
	name, tag := splitRef(dst)
	if tag == "" {
		tag = "latest"
	}
	q := url.Values{"repo": {name}, "tag": {tag}}
	return c.call(ctx, request{op: "tag image " + src + " as " + dst, method: http.MethodPost, path: "/images/" + src + "/tag", query: q}, nil)
}

// ImageRemove deletes a reference. The Engine refuses while a container still
// uses the image; that refusal is returned as is, not forced. A missing
// reference matches ErrNotFound.
func (c *Client) ImageRemove(ctx context.Context, ref string) error {
	if err := checkRef(ref); err != nil {
		return err
	}
	return c.call(ctx, request{op: "remove image " + ref, method: http.MethodDelete, path: "/images/" + ref}, nil)
}

// tarDir packs a build context directory. Only regular files and directories
// are sent; a symbolic link or any other special file is an error, because the
// Engine would follow a link to wherever it points.
func tarDir(dir string) (*bytes.Reader, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("build context %q is not an absolute path", dir)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("build context entry %s is not a regular file or directory", rel)
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("build context: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}

func tarOf(name string, content []byte) (*bytes.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}
