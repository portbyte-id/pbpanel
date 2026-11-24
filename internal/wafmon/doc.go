// Package wafmon turns the logs nginx, ModSecurity and open-appsec already write
// into live traffic statistics and a searchable list of WAF events.
//
// A Collector polls the per-site log directory, tails each `<domain>.access.log`
// and `<domain>.waf-audit.log` by byte offset, and keeps what it parsed in
// bounded in-memory structures: 10-second buckets for the last hour per site, and
// two rings of the most recent requests and WAF events. Nothing is written to
// disk, so a daemon restart starts from a backfill of the log tails instead of
// from nothing.
//
// It also tails the open-appsec agent's security log, which is one JSON-lines
// file for every site. Its events name their own host, so they route to a site
// from the event rather than from the file, and land in the same event ring with
// the same per-site counters. A host without the agent has no log directory, and
// that is not an error: it simply has no open-appsec events.
package wafmon
