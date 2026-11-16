package docker

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Stream types in the 8-byte frame header of a multiplexed stream
// (https://docs.docker.com/reference/api/engine/#tag/Container/operation/ContainerAttach).
const (
	streamStdin  = 0
	streamStdout = 1
	streamStderr = 2

	frameHeaderLen = 8
	// maxFrame rejects a header that claims an absurd payload: it means the
	// stream is not multiplexed (a TTY container) or is corrupt, and reading on
	// would interleave garbage into command output.
	maxFrame = 16 << 20
)

// demux splits a multiplexed attach stream into stdout and stderr. It returns
// nil at a clean end of stream.
func demux(src io.Reader, stdout, stderr io.Writer) error {
	var header [frameHeaderLen]byte
	for {
		if _, err := io.ReadFull(src, header[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("docker: read stream header: %w", err)
		}
		size := binary.BigEndian.Uint32(header[4:])
		if size > maxFrame {
			return fmt.Errorf("docker: stream frame of %d bytes: not a multiplexed stream", size)
		}
		var dst io.Writer
		switch header[0] {
		case streamStdin, streamStdout:
			dst = stdout
		case streamStderr:
			dst = stderr
		default:
			return fmt.Errorf("docker: unknown stream type %d: not a multiplexed stream", header[0])
		}
		if _, err := io.CopyN(dst, src, int64(size)); err != nil {
			return fmt.Errorf("docker: read stream frame: %w", err)
		}
	}
}

// capBuffer keeps the first limit bytes written to it and counts the rest. A
// validator that prints megabytes must not grow the daemon's memory, but the
// caller needs to know the output it shows is incomplete.
type capBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		if len(p) <= room {
			b.buf = append(b.buf, p...)
		} else {
			b.buf = append(b.buf, p[:room]...)
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

// message is one line of the JSON progress stream that pull and build answer
// with. The Engine reports failure inside a 200 response, as a line carrying
// `error`, so a caller that only checks the status code sees success.
type message struct {
	Status string `json:"status"`
	ID     string `json:"id"`
	Stream string `json:"stream"`
	Error  string `json:"error"`
	Detail struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
}

func (m message) err() error {
	switch {
	case m.Detail.Message != "":
		return errors.New(m.Detail.Message)
	case m.Error != "":
		return errors.New(m.Error)
	}
	return nil
}

// readMessages decodes a JSON progress stream, handing each message to fn, and
// returns the first in-stream error.
func readMessages(src io.Reader, fn func(message)) error {
	dec := json.NewDecoder(bufio.NewReader(src))
	for {
		var m message
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode progress stream: %w", err)
		}
		if err := m.err(); err != nil {
			return err
		}
		fn(m)
	}
}
