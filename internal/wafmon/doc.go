// Package wafmon turns the logs nginx and ModSecurity already write into live
// traffic statistics and a searchable list of WAF events.
//
// A Collector polls the per-site log directory, tails each `<domain>.access.log`
// and `<domain>.waf-audit.log` by byte offset, and keeps what it parsed in
// bounded in-memory structures: 10-second buckets for the last hour per site, and
// two rings of the most recent requests and WAF events. Nothing is written to
// disk, so a daemon restart starts from a backfill of the log tails instead of
// from nothing.
package wafmon
