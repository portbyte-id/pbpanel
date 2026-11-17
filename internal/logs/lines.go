package logs

import (
	"io"
	"path/filepath"
)

// maxLine caps one line. The rest of a longer line is dropped and a marker
// appended, so a binary blob or a minified payload cannot exhaust memory.
const maxLine = 256 << 10

const truncMarker = " …[truncated]"

func isAbs(p string) bool { return filepath.IsAbs(p) }

// splitter turns a byte stream into lines incrementally, holding a partial last
// line until its newline arrives.
type splitter struct {
	pending []byte
	over    bool
}

func (s *splitter) add(p []byte) {
	if s.over || len(p) == 0 {
		return
	}
	if room := maxLine - len(s.pending); len(p) > room {
		s.pending = append(s.pending, p[:room]...)
		s.over = true
		return
	}
	s.pending = append(s.pending, p...)
}

func (s *splitter) take() string {
	line := s.pending
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	out := string(line)
	if s.over {
		out += truncMarker
	}
	s.pending = s.pending[:0]
	s.over = false
	return out
}

func (s *splitter) feed(p []byte, emit func(string) error) error {
	for len(p) > 0 {
		i := indexNL(p)
		if i < 0 {
			s.add(p)
			return nil
		}
		s.add(p[:i])
		if err := emit(s.take()); err != nil {
			return err
		}
		p = p[i+1:]
	}
	return nil
}

// flush emits the partial last line, if any.
func (s *splitter) flush(emit func(string) error) error {
	if len(s.pending) == 0 && !s.over {
		return nil
	}
	return emit(s.take())
}

func indexNL(p []byte) int {
	for i, b := range p {
		if b == '\n' {
			return i
		}
	}
	return -1
}

// readLines feeds r to emit until EOF, including a final unterminated line.
// check, if non-nil, is polled between reads so a caller can abort.
func readLines(r io.Reader, emit func(string) error, check func() error) error {
	var sp splitter
	buf := make([]byte, 64<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if e := sp.feed(buf[:n], emit); e != nil {
				return e
			}
		}
		if err == io.EOF {
			return sp.flush(emit)
		}
		if err != nil {
			return err
		}
		if check != nil {
			if e := check(); e != nil {
				return e
			}
		}
	}
}
