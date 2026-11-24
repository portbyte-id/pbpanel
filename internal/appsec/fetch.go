package appsec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The pinned agent archive is about 59 MB; this bounds a response that is not it.
const maxArchiveBytes = 256 << 20

// DefaultFetch downloads rel into dest and refuses anything whose SHA-256 is not
// the pinned one. Only https is allowed.
//
// The download goes to dest+".part" and is renamed only once the digest matches,
// so a short or replaced response leaves nothing at dest that could be unpacked.
func DefaultFetch(ctx context.Context, rel Release, dest string) error {
	if !strings.HasPrefix(rel.URL, "https://") {
		return fmt.Errorf("appsec: refusing to download %s: only https is allowed", rel.URL)
	}
	return fetchFrom(ctx, rel.URL, rel.SHA256, dest)
}

// fetchFrom is DefaultFetch without the scheme check. The scheme is the caller's
// rule; the download, the digest and the rename are this function's, and tests run
// them against a local server because an https origin cannot be faked cheaply.
func fetchFrom(ctx context.Context, url, wantSHA, dest string) error {
	client := &http.Client{Timeout: 15 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("appsec: download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("appsec: download %s: HTTP %d", url, resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	part := dest + ".part"
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("appsec: create %s: %w", part, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxArchiveBytes+1))
	closeErr := f.Close()
	switch {
	case err != nil:
		_ = os.Remove(part)
		return fmt.Errorf("appsec: download %s: %w", url, err)
	case closeErr != nil:
		_ = os.Remove(part)
		return fmt.Errorf("appsec: write %s: %w", part, closeErr)
	case n > maxArchiveBytes:
		_ = os.Remove(part)
		return fmt.Errorf("appsec: %s is larger than %d bytes", url, maxArchiveBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != strings.ToLower(wantSHA) {
		_ = os.Remove(part)
		return fmt.Errorf("appsec: sha256 of %s is %s, the panel pins %s; nothing was installed. "+
			"The vendor republishes this URL for every release, so a mismatch usually means a newer agent "+
			"has been published: a panel update carries the new pin", url, got, wantSHA)
	}
	if err := os.Chmod(part, 0o600); err != nil {
		_ = os.Remove(part)
		return err
	}
	if err := os.Rename(part, dest); err != nil {
		_ = os.Remove(part)
		return err
	}
	return nil
}

// fetchFromForTest is fetchFrom under a name the tests use: it says at the call
// site that the scheme rule is deliberately bypassed.
func fetchFromForTest(ctx context.Context, url, wantSHA, dest string) error {
	return fetchFrom(ctx, url, wantSHA, dest)
}

// sha256Hex is the hex SHA-256 of s, for tests that need a digest to match.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
