package wafmon

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// MaxLine caps one log line. A longer line is skipped, so a blob in a user agent
// cannot make the collector hold megabytes.
const MaxLine = 64 << 10

const accessTimeLayout = "02/Jan/2006:15:04:05 -0700"

// Request is one access-log line.
type Request struct {
	Seq       uint64    `json:"seq"`
	Time      time.Time `json:"time"`
	Site      string    `json:"site"`
	IP        string    `json:"ip"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Status    int       `json:"status"`
	Bytes     int64     `json:"bytes"`
	MS        float64   `json:"ms"`
	Referer   string    `json:"referer"`
	Agent     string    `json:"agent"`
	RequestID string    `json:"request_id"`

	// hasMS is false for a line in the plain `combined` format.
	hasMS bool
}

// ParseAccess parses a line in either the `pbpanel` format
//
//	$remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent
//	"$http_referer" "$http_user_agent" rt=$request_time rid=$request_id
//
// or the plain `combined` one (the same without the two trailing fields). A line
// it cannot read reports false. The time is zero when it cannot be parsed.
func ParseAccess(line string) (Request, bool) {
	var r Request
	ip, rest, ok := strings.Cut(line, " ")
	if !ok || ip == "" {
		return r, false
	}
	// "- <user> [" : the identd dash and the remote user.
	if _, rest, ok = strings.Cut(rest, " "); !ok {
		return r, false
	}
	if _, rest, ok = strings.Cut(rest, " "); !ok {
		return r, false
	}
	if !strings.HasPrefix(rest, "[") {
		return r, false
	}
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		return r, false
	}
	if t, err := time.Parse(accessTimeLayout, rest[1:end]); err == nil {
		r.Time = t
	}
	rest = strings.TrimPrefix(rest[end+1:], " ")

	req, rest, ok := quoted(rest)
	if !ok {
		return r, false
	}
	rest = strings.TrimPrefix(rest, " ")
	statusTok, rest, ok := strings.Cut(rest, " ")
	if !ok {
		return r, false
	}
	bytesTok, rest, _ := strings.Cut(rest, " ")
	status, err := strconv.Atoi(statusTok)
	if err != nil || status < 100 || status > 999 {
		return r, false
	}
	n, err := strconv.ParseInt(bytesTok, 10, 64)
	if err != nil {
		n = 0
	}
	referer, rest, ok := quoted(rest)
	if !ok {
		return r, false
	}
	rest = strings.TrimPrefix(rest, " ")
	agent, rest, ok := quoted(rest)
	if !ok {
		return r, false
	}
	for _, f := range strings.Fields(rest) {
		switch {
		case strings.HasPrefix(f, "rt="):
			if s, err := strconv.ParseFloat(f[3:], 64); err == nil && s >= 0 {
				r.MS, r.hasMS = s*1000, true
			}
		case strings.HasPrefix(f, "rid="):
			r.RequestID = f[4:]
		}
	}

	r.IP, r.Status, r.Bytes, r.Referer, r.Agent = ip, status, n, referer, agent
	// "GET /x HTTP/1.1"; anything else (a TLS handshake sent to port 80) keeps
	// the raw text as the path so the request still counts.
	parts := strings.Split(req, " ")
	if len(parts) == 3 && parts[0] != "" {
		r.Method, r.Path = parts[0], parts[1]
	} else {
		r.Path = req
	}
	return r, true
}

// quoted reads a double-quoted field at the start of s and returns its content
// and the text after the closing quote.
func quoted(s string) (string, string, bool) {
	if !strings.HasPrefix(s, `"`) {
		return "", s, false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\'):
			b.WriteByte(s[i+1])
			i++
		case c == '"':
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(c)
		}
	}
	return "", s, false
}

// Match is one rule that matched a transaction.
type Match struct {
	RuleID   string   `json:"rule_id"`
	Message  string   `json:"message"`
	Severity string   `json:"severity"`
	Data     string   `json:"data"`
	Tags     []string `json:"tags"`
	File     string   `json:"file"`
	Line     int      `json:"line"`

	rank int
}

// Event is one ModSecurity audit-log transaction.
type Event struct {
	Seq       uint64    `json:"seq"`
	Time      time.Time `json:"time"`
	Site      string    `json:"site"`
	IP        string    `json:"ip"`
	Method    string    `json:"method"`
	URI       string    `json:"uri"`
	Status    int       `json:"status"`
	RequestID string    `json:"request_id"`
	Action    string    `json:"action"`
	RuleID    string    `json:"rule_id"`
	Message   string    `json:"message"`
	Severity  string    `json:"severity"`
	Matches   []Match   `json:"matches"`
}

// Event actions.
const (
	ActionBlocked  = "blocked"
	ActionDetected = "detected"
)

// isAnomalyRule is CRS's summary rules: they decide the verdict but say nothing
// about what was found, so a row shows the specific rule instead.
func isAnomalyRule(id string) bool { return id == "949110" || id == "980130" }

type auditDoc struct {
	Transaction *struct {
		ClientIP  string `json:"client_ip"`
		TimeStamp string `json:"time_stamp"`
		UniqueID  string `json:"unique_id"`
		Request   struct {
			Method string `json:"method"`
			URI    string `json:"uri"`
		} `json:"request"`
		Response struct {
			HTTPCode any `json:"http_code"`
		} `json:"response"`
		Messages []struct {
			Message string `json:"message"`
			Details struct {
				RuleID     any      `json:"ruleId"`
				Severity   any      `json:"severity"`
				Data       string   `json:"data"`
				Tags       []string `json:"tags"`
				File       string   `json:"file"`
				LineNumber any      `json:"lineNumber"`
			} `json:"details"`
		} `json:"messages"`
	} `json:"transaction"`
}

// ParseAudit parses one JSON line of ModSecurity v3's audit log. Numbers may
// arrive as strings and the other way round, so every numeric field is read
// loosely. A line that is not a transaction reports false.
//
// The action is "blocked" when the response was an error and either CRS's
// anomaly-score rule matched or the status is 403. In DetectionOnly mode nothing
// is blocked, and an application's own 404 beside a low-score match must not be
// called a block, so those stay "detected".
func ParseAudit(line string) (Event, bool) {
	var d auditDoc
	if err := json.Unmarshal([]byte(line), &d); err != nil || d.Transaction == nil {
		return Event{}, false
	}
	t := d.Transaction
	e := Event{IP: t.ClientIP, Method: t.Request.Method, URI: t.Request.URI, RequestID: t.UniqueID, Matches: []Match{}}
	if code, ok := toInt(t.Response.HTTPCode); ok {
		e.Status = code
	}
	// ModSecurity writes a zoneless ctime stamp in the process's local time, and
	// nginx runs in a container whose clock is UTC, so UTC is what it means.
	for _, layout := range []string{"Mon Jan _2 15:04:05 2006", time.RFC3339} {
		if ts, err := time.ParseInLocation(layout, t.TimeStamp, time.UTC); err == nil {
			e.Time = ts
			break
		}
	}

	var anomaly bool
	best := -1
	for _, m := range t.Messages {
		id := toString(m.Details.RuleID)
		name, rank := severity(m.Details.Severity)
		ln, _ := toInt(m.Details.LineNumber)
		tags := m.Details.Tags
		if tags == nil {
			tags = []string{}
		}
		e.Matches = append(e.Matches, Match{
			RuleID: id, Message: m.Message, Severity: name, Data: m.Details.Data,
			Tags: tags, File: m.Details.File, Line: ln, rank: rank,
		})
		anomaly = anomaly || isAnomalyRule(id)
		// The most severe specific rule; the first of equals wins.
		if i := len(e.Matches) - 1; !isAnomalyRule(id) && (best < 0 || rank < e.Matches[best].rank) {
			best = i
		}
	}
	if best < 0 && len(e.Matches) > 0 {
		best = 0
	}
	if best >= 0 {
		m := e.Matches[best]
		e.RuleID, e.Message, e.Severity = m.RuleID, m.Message, m.Severity
	}
	e.Action = ActionDetected
	if e.Status >= 400 && len(e.Matches) > 0 && (anomaly || e.Status == 403) {
		e.Action = ActionBlocked
	}
	return e, true
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	}
	return 0, false
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	}
	return ""
}

// severityNames are ModSecurity's levels 0-7, with emergency and alert folded
// into critical because the UI offers one worst level.
var severityNames = [...]string{"critical", "critical", "critical", "error", "warning", "notice", "info", "debug"}

// severityRank is the rank of a level named in words; critical is level 2.
var severityRank = map[string]int{"critical": 2, "error": 3, "warning": 4, "notice": 5, "info": 6, "debug": 7}

// severity maps ModSecurity's 0-7 scale, as a number or a word, to a name and a
// rank where lower is worse. An unknown value ranks last.
func severity(v any) (string, int) {
	if n, ok := toInt(v); ok && n >= 0 && n < len(severityNames) {
		return severityNames[n], n
	}
	if s, ok := v.(string); ok {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "emergency" || s == "alert" {
			s = "critical"
		}
		if rank, ok := severityRank[s]; ok {
			return s, rank
		}
	}
	return "unknown", len(severityNames)
}
