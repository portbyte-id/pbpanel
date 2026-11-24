package geoip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Where the database comes from and what the license asks products to show.
const (
	// DownloadBase is the DB-IP directory of free monthly country databases.
	DownloadBase = "https://download.db-ip.com/free/"

	// Attribution is the credit DB-IP's license asks for; AttributionURL is the
	// link that goes with it.
	Attribution    = "IP Geolocation by DB-IP"
	AttributionURL = "https://db-ip.com"
	LicenseURL     = "https://creativecommons.org/licenses/by/4.0/"

	filePrefix = "dbip-country-lite-"
	fileSuffix = ".csv.gz"

	// maxDownload bounds one database. The monthly file is about 5 MB.
	maxDownload = 64 << 20
)

// ErrNotPublished is returned when the publisher has no database for the month.
var ErrNotPublished = errors.New("geoip: the database for that month is not published")

// Status is what the panel can say about the installed database.
type Status struct {
	Installed      bool   `json:"installed"`
	Month          string `json:"month"`
	Entries        int    `json:"entries"`
	Attribution    string `json:"attribution"`
	AttributionURL string `json:"attribution_url"`
	LicenseURL     string `json:"license_url"`
}

// Store holds the installed country database and replaces it when a newer one
// is fetched. It is safe for concurrent use.
type Store struct {
	dir    string
	base   string
	now    func() time.Time
	client *http.Client

	mu    sync.RWMutex
	db    *DB
	month string
}

// NewStore returns a store that keeps its database in dir. An installed database
// is loaded at once; a missing or damaged one leaves the store empty until Fetch.
func NewStore(dir string) *Store {
	s := &Store{
		dir:  dir,
		base: DownloadBase,
		now:  time.Now,
		client: &http.Client{
			Timeout:       3 * time.Minute,
			CheckRedirect: checkRedirect,
		},
	}
	if month, path, ok := installed(dir); ok {
		if db, err := Load(path); err == nil {
			s.db, s.month = db, month
		}
	}
	return s
}

// Country returns the country of addr, or false when there is no database or no
// range covers the address.
func (s *Store) Country(addr netip.Addr) (string, bool) {
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	return db.Country(addr)
}

// Status describes the installed database.
func (s *Store) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Status{
		Installed: s.db != nil, Month: s.month,
		Attribution: Attribution, AttributionURL: AttributionURL, LicenseURL: LicenseURL,
	}
	if s.db != nil {
		st.Entries = s.db.Entries()
	}
	return st
}

// Fetch downloads the database for the current month, or the last month when the
// publisher has not put the current one out yet. The file is checked by parsing
// it before it replaces the installed one, so a damaged download changes nothing.
func (s *Store) Fetch(ctx context.Context) (Status, error) {
	now := s.now().UTC()
	months := []time.Time{
		time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
		time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, time.UTC),
	}
	for i, m := range months {
		month := m.Format("2006-01")
		name := filePrefix + month + fileSuffix
		tmp, err := s.download(ctx, s.base+name)
		if errors.Is(err, ErrNotPublished) && i == 0 {
			continue
		}
		if err != nil {
			return s.Status(), err
		}
		db, err := Load(tmp)
		if err != nil {
			_ = os.Remove(tmp)
			return s.Status(), fmt.Errorf("geoip: the downloaded database does not parse: %w", err)
		}
		final := filepath.Join(s.dir, name)
		if err := os.Rename(tmp, final); err != nil {
			_ = os.Remove(tmp)
			return s.Status(), fmt.Errorf("geoip: install the database: %w", err)
		}
		removeOthers(s.dir, name)

		s.mu.Lock()
		s.db, s.month = db, month
		s.mu.Unlock()
		return s.Status(), nil
	}
	return s.Status(), ErrNotPublished
}

// download streams one database into a temporary file in the store's directory
// and returns its path. The caller installs or removes it.
func (s *Store) download(ctx context.Context, u string) (string, error) {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return "", fmt.Errorf("geoip: create %s: %w", s.dir, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "PB-Panel")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("geoip: download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrNotPublished
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("geoip: download: the server answered %s", resp.Status)
	}

	f, err := os.CreateTemp(s.dir, "dbip-*.tmp")
	if err != nil {
		return "", fmt.Errorf("geoip: create temporary file: %w", err)
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxDownload+1))
	closeErr := f.Close()
	if copyErr == nil && n > maxDownload {
		copyErr = fmt.Errorf("the download is larger than %d bytes", maxDownload)
	}
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("geoip: download: %w", err)
	}
	return f.Name(), nil
}

// checkRedirect follows redirects only to the publisher over HTTPS.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return errors.New("geoip: too many redirects")
	}
	if req.URL.Scheme != "https" || req.URL.Host != mustHost(DownloadBase) {
		return fmt.Errorf("geoip: refusing to follow a redirect to %s", req.URL.Host)
	}
	return nil
}

func mustHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u.Host
}

// installed returns the newest database in dir and its month.
func installed(dir string) (month, path string, ok bool) {
	matches, _ := filepath.Glob(filepath.Join(dir, filePrefix+"*"+fileSuffix))
	if len(matches) == 0 {
		return "", "", false
	}
	sort.Strings(matches)
	path = matches[len(matches)-1]
	name := filepath.Base(path)
	month = strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
	return month, path, true
}

// removeOthers deletes the databases in dir other than keep.
func removeOthers(dir, keep string) {
	matches, _ := filepath.Glob(filepath.Join(dir, filePrefix+"*"+fileSuffix))
	for _, m := range matches {
		if filepath.Base(m) != keep {
			_ = os.Remove(m)
		}
	}
}
