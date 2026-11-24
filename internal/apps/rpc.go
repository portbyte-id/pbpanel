package apps

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Fault is an XML-RPC fault: supervisord's way of saying a call failed.
type Fault struct {
	Code   int
	String string
}

func (f *Fault) Error() string { return fmt.Sprintf("supervisord fault %d: %s", f.Code, f.String) }

// Fault codes of the supervisor protocol the panel acts on.
const (
	faultBadName        = 10
	faultAlreadyStarted = 60
	faultNotRunning     = 70
)

// Errors the client maps faults to.
var (
	ErrNoProcess      = errors.New("apps: supervisord has no such program")
	ErrAlreadyRunning = errors.New("apps: the app is already running")
	ErrNotRunning     = errors.New("apps: the app is not running")
	ErrStartFailed    = errors.New("apps: the app failed to start")
)

// rpcTimeout bounds one call. startProcess with wait=true holds the call open for
// the program's startsecs, so it must be longer than that.
const rpcTimeout = 30 * time.Second

// Client speaks XML-RPC to supervisord.
type Client struct {
	http *http.Client
	url  string
}

// NewClient returns a client for supervisord's Unix socket.
func NewClient(socket string) *Client {
	return &Client{
		url: "http://supervisord/RPC2",
		http: &http.Client{
			Timeout: rpcTimeout,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socket)
				},
				DisableKeepAlives: true,
			},
		},
	}
}

// newClient is the test seam: a client for any HTTP endpoint.
func newClient(h *http.Client, url string) *Client { return &Client{http: h, url: url} }

// call performs one method call and returns its first result value.
func (c *Client) call(ctx context.Context, method string, params ...any) (any, error) {
	body, err := encodeCall(method, params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoSupervisor, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("supervisord %s: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("supervisord %s: HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	v, fault, err := decodeResponse(raw)
	if err != nil {
		return nil, fmt.Errorf("supervisord %s: %w", method, err)
	}
	if fault != nil {
		return nil, fault
	}
	return v, nil
}

// encodeCall renders a methodCall. Parameters are strings, ints and bools: the
// only types the supported methods take.
func encodeCall(method string, params []any) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?><methodCall><methodName>`)
	if err := xml.EscapeText(&b, []byte(method)); err != nil {
		return nil, err
	}
	b.WriteString(`</methodName><params>`)
	for _, p := range params {
		b.WriteString(`<param><value>`)
		switch v := p.(type) {
		case string:
			b.WriteString(`<string>`)
			if err := xml.EscapeText(&b, []byte(v)); err != nil {
				return nil, err
			}
			b.WriteString(`</string>`)
		case int:
			fmt.Fprintf(&b, `<int>%d</int>`, v)
		case bool:
			if v {
				b.WriteString(`<boolean>1</boolean>`)
			} else {
				b.WriteString(`<boolean>0</boolean>`)
			}
		default:
			return nil, fmt.Errorf("unsupported XML-RPC parameter type %T", p)
		}
		b.WriteString(`</value></param>`)
	}
	b.WriteString(`</params></methodCall>`)
	return b.Bytes(), nil
}

// decodeResponse parses a methodResponse into a generic value: string, int, bool,
// float64, []any or map[string]any. A fault comes back as the second result.
func decodeResponse(raw []byte) (any, *Fault, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("malformed XML-RPC response: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "fault":
			v, err := nextValue(dec)
			if err != nil {
				return nil, nil, err
			}
			m, _ := v.(map[string]any)
			f := &Fault{String: asString(m["faultString"])}
			f.Code, _ = asInt(m["faultCode"])
			return nil, f, nil
		case "param":
			v, err := nextValue(dec)
			if err != nil {
				return nil, nil, err
			}
			return v, nil, nil
		}
	}
}

// nextValue reads up to and including the next <value> element.
func nextValue(dec *xml.Decoder) (any, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "value" {
			return readValue(dec)
		}
	}
}

// readValue reads the content of a <value> whose start tag was consumed, through
// its end tag.
func readValue(dec *xml.Decoder) (any, error) {
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			// <value>text</value> is a string.
			return text.String(), nil
		case xml.StartElement:
			v, err := readTyped(dec, t.Name.Local)
			if err != nil {
				return nil, err
			}
			if err := skipToEnd(dec, "value"); err != nil {
				return nil, err
			}
			return v, nil
		}
	}
}

func readTyped(dec *xml.Decoder, kind string) (any, error) {
	switch kind {
	case "string", "base64", "dateTime.iso8601":
		return elementText(dec)
	case "int", "i4", "i8":
		s, err := elementText(dec)
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("bad integer %q", s)
		}
		return n, nil
	case "boolean":
		s, err := elementText(dec)
		return strings.TrimSpace(s) == "1", err
	case "double":
		s, err := elementText(dec)
		if err != nil {
			return nil, err
		}
		return strconv.ParseFloat(strings.TrimSpace(s), 64)
	case "nil":
		return nil, skipToEnd(dec, "nil")
	case "array":
		out := []any{}
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "value" {
					v, err := readValue(dec)
					if err != nil {
						return nil, err
					}
					out = append(out, v)
				}
			case xml.EndElement:
				if t.Name.Local == "array" {
					return out, nil
				}
			}
		}
	case "struct":
		out := map[string]any{}
		var name string
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "name":
					name, err = elementText(dec)
					if err != nil {
						return nil, err
					}
				case "value":
					v, err := readValue(dec)
					if err != nil {
						return nil, err
					}
					out[name] = v
				}
			case xml.EndElement:
				if t.Name.Local == "struct" {
					return out, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("unsupported XML-RPC type <%s>", kind)
}

// elementText reads the text of the current element through its end tag.
func elementText(dec *xml.Decoder) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			return b.String(), nil
		}
	}
}

// skipToEnd consumes tokens through the end tag named name.
func skipToEnd(dec *xml.Decoder, name string) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == name {
			return nil
		}
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	}
	return 0, false
}

// unwrap returns the value a reply carries. The source of the Go supervisord does
// not settle whether a call that returns one value sends it bare or inside a
// one-member struct, so a struct with a single member is unwrapped.
func unwrap(v any) any {
	if m, ok := v.(map[string]any); ok && len(m) == 1 {
		for _, inner := range m {
			return inner
		}
	}
	return v
}

// mapFault turns a fault the caller has a name for into its sentinel.
func mapFault(err error) error {
	var f *Fault
	if !errors.As(err, &f) {
		return err
	}
	switch f.Code {
	case faultBadName:
		return fmt.Errorf("%w: %s", ErrNoProcess, f.String)
	case faultAlreadyStarted:
		return fmt.Errorf("%w: %s", ErrAlreadyRunning, f.String)
	case faultNotRunning:
		return fmt.Errorf("%w: %s", ErrNotRunning, f.String)
	}
	return err
}
