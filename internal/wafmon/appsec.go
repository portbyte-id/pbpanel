package wafmon

import (
	"encoding/json"
	"net"
	"strings"
	"time"
)

// Engine names the inspection engine an event came from. The values are the ones
// internal/waf's EngineKind uses, so the UI can compare them; the monitor keeps
// its own constants because it must not depend on the firewall package.
const (
	EngineModSecurity = "modsecurity"
	EngineOpenAppsec  = "open-appsec"
)

// DefaultAppsecDir is where the open-appsec agent keeps its logs, per step 0's
// findings: the directory is 700 root:root, the files 644 root:root, and the
// panel's reader runs as root.
const DefaultAppsecDir = "/var/log/nano_agent"

// appsecLogPrefix names the agent's HTTP transaction handler log. The live file
// may be the bare name or a vendor-rotated `…handler.log<N>`; `…handler.dbg*`
// files are the agent's own debug output and are not read.
const appsecLogPrefix = "cp-nano-http-transaction-handler.log"

// appsecTimeLayout is the agent's timestamp: local time, milliseconds, no zone.
const appsecTimeLayout = "2006-01-02T15:04:05.000"

// appsecEvent is one line of the agent's security log. Only the fields the
// monitor maps are read; the agent writes more.
type appsecEvent struct {
	EventTime     string `json:"eventTime"`
	EventSeverity string `json:"eventSeverity"`
	EventData     *struct {
		EventReferenceID string `json:"eventReferenceId"`
		EventConfidence  string `json:"eventConfidence"`
		SourceIP         string `json:"sourceIP"`
		HTTPHostName     string `json:"httpHostName"`
		HTTPMethod       string `json:"httpMethod"`
		HTTPURIPath      string `json:"httpUriPath"`
		HTTPURIQuery     string `json:"httpUriQuery"`
		SecurityAction   string `json:"securityAction"`
		WaapIncidentType string `json:"waapIncidentType"`
		MatchedSample    string `json:"matchedSample"`
		MatchedLocation  string `json:"matchedLocation"`
		MatchedParameter string `json:"matchedParameter"`
	} `json:"eventData"`
}

// ParseOpenAppsec parses one line of the open-appsec agent's security log, which
// is JSON Lines: one object per request an incident fired on. A line for anything
// else the agent logs (a policy load, a service start) and anything that is not
// JSON report false.
//
// The event names its own host, and the agent's log holds every site's events, so
// the site is set here rather than by the file the line came from as it is for
// ModSecurity: eventData.httpHostName is the Host header, routed through
// ValidDomain like a site log's file name. Site is "" when the host is absent or
// cannot be a site name, and the event then belongs to no site; it is still kept.
//
// eventTime carries no zone because the agent writes the host's local time, and
// the panel runs on the same host, so it is read as local time. The sandbox this
// was checked on runs UTC, where local and UTC agree, so the two cannot be told
// apart there; on a host with a non-UTC zone they would differ.
func ParseOpenAppsec(line string) (Event, bool) {
	var d appsecEvent
	if err := json.Unmarshal([]byte(line), &d); err != nil || d.EventData == nil {
		return Event{}, false
	}
	x := d.EventData
	if x.WaapIncidentType == "" && x.SecurityAction == "" {
		return Event{}, false
	}
	e := Event{
		Engine:     EngineOpenAppsec,
		Site:       appsecSite(x.HTTPHostName),
		IP:         x.SourceIP,
		Method:     x.HTTPMethod,
		URI:        appsecURI(x.HTTPURIPath, x.HTTPURIQuery),
		RequestID:  x.EventReferenceID,
		Confidence: x.EventConfidence,
		AttackType: x.WaapIncidentType,
		Parameter:  appsecParameter(x.MatchedParameter, x.MatchedLocation),
	}
	e.Severity = appsecSeverity(d.EventSeverity)
	e.Action = ActionDetected
	if strings.EqualFold(strings.TrimSpace(x.SecurityAction), "prevent") {
		e.Action = ActionBlocked
	}
	for _, layout := range []string{appsecTimeLayout, time.RFC3339} {
		if ts, err := time.ParseInLocation(layout, d.EventTime, time.Local); err == nil {
			e.Time = ts
			break
		}
	}
	// open-appsec has no rule ids, so the event carries one synthetic match for
	// the detail rows: what fired, its severity, and the sample that matched.
	e.Message = x.WaapIncidentType
	if e.Message == "" {
		e.Message = x.SecurityAction
	}
	e.Matches = []Match{{Message: e.Message, Severity: e.Severity, Data: x.MatchedSample, Tags: []string{}}}
	return e, true
}

// appsecSite turns the event's Host header into the site name it routes to: the
// port is dropped, the name lowercased, and a trailing dot removed before the
// same ValidDomain check a site log's file name is held to.
func appsecSite(host string) string {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !ValidDomain(host) {
		return ""
	}
	return host
}

// appsecURI joins the path and the query the way the access log writes them; an
// event can carry only a query.
func appsecURI(path, query string) string {
	switch {
	case query == "":
		return path
	case path == "":
		return "?" + query
	}
	return path + "?" + query
}

// appsecParameter is the request field that matched, with where it was matched.
// The plan maps matchedParameter and matchedLocation onto the one field the event
// page shows.
func appsecParameter(param, location string) string {
	param, location = strings.TrimSpace(param), strings.TrimSpace(location)
	switch {
	case param == "":
		return location
	case location == "":
		return param
	}
	return param + " (" + location + ")"
}

// appsecSeverities maps the agent's severity words onto the monitor's. Step 0
// captured only "Critical"; the other levels follow the usual five-level scale.
var appsecSeverities = map[string]string{
	"critical": "critical", "high": "error", "medium": "warning",
	"low": "notice", "info": "info", "informational": "info", "debug": "debug",
}

// appsecSeverity lowercases the agent's severity; a word the map does not hold is
// "unknown", as it is for ModSecurity.
func appsecSeverity(v string) string {
	if s, ok := appsecSeverities[strings.ToLower(strings.TrimSpace(v))]; ok {
		return s
	}
	return "unknown"
}
