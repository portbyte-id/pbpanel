// Package geoip finds the country of an IP address from the DB-IP Lite country
// database: a free download, licensed CC BY 4.0, with no account or key. The
// database is a CSV of address ranges, `start,end,country`. It is read whole into
// memory, sorted, and searched by binary search, so a lookup costs a few
// comparisons and no network.
//
// The license asks that products using the data credit DB-IP; the dashboard
// shows that credit beside the map.
package geoip

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
	"strings"
)

// ErrEmpty is returned when a database has no ranges, which means it was not one.
var ErrEmpty = errors.New("geoip: the database has no address ranges")

type span struct {
	from, to netip.Addr
	cc       [2]byte
}

// DB is a loaded country database.
type DB struct {
	spans []span
}

// Entries is how many address ranges the database holds.
func (db *DB) Entries() int { return len(db.spans) }

// Country returns the two-letter ISO country code of addr, upper case. It
// reports false when no range covers the address, which includes every private
// and loopback address the database does not list.
func (db *DB) Country(addr netip.Addr) (string, bool) {
	if db == nil || len(db.spans) == 0 || !addr.IsValid() {
		return "", false
	}
	addr = addr.Unmap()
	// The first span that starts after addr, less one, is the last that might hold it.
	i := sort.Search(len(db.spans), func(i int) bool { return db.spans[i].from.Compare(addr) > 0 }) - 1
	if i < 0 {
		return "", false
	}
	s := db.spans[i]
	if addr.Compare(s.to) > 0 {
		return "", false
	}
	return string(s.cc[:]), true
}

// Parse reads a DB-IP country CSV, plain or gzipped. A malformed row is an error:
// the file comes from one publisher, so a bad row means a bad download.
func Parse(r io.Reader) (*DB, error) {
	br := bufio.NewReader(r)
	if magic, err := br.Peek(2); err == nil && bytes.Equal(magic, []byte{0x1f, 0x8b}) {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("geoip: open gzip: %w", err)
		}
		defer func() { _ = gz.Close() }()
		br = bufio.NewReader(gz)
	}

	cr := csv.NewReader(br)
	cr.FieldsPerRecord = 3
	cr.ReuseRecord = true
	var spans []span
	for line := 1; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("geoip: line %d: %w", line, err)
		}
		from, err := netip.ParseAddr(strings.TrimSpace(rec[0]))
		if err != nil {
			return nil, fmt.Errorf("geoip: line %d: start address: %w", line, err)
		}
		to, err := netip.ParseAddr(strings.TrimSpace(rec[1]))
		if err != nil {
			return nil, fmt.Errorf("geoip: line %d: end address: %w", line, err)
		}
		cc := strings.ToUpper(strings.TrimSpace(rec[2]))
		if len(cc) != 2 || from.Is4() != to.Is4() || from.Compare(to) > 0 {
			return nil, fmt.Errorf("geoip: line %d: not a country range", line)
		}
		s := span{from: from.Unmap(), to: to.Unmap()}
		copy(s.cc[:], cc)
		spans = append(spans, s)
	}
	if len(spans) == 0 {
		return nil, ErrEmpty
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].from.Compare(spans[j].from) < 0 })
	return &DB{spans: spans}, nil
}

// Load reads the database file at path.
func Load(path string) (*DB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}
