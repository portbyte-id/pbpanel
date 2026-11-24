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
// It also tails the open-appsec agent's security logs. There is one log per
// agent worker, named `cp-nano-http-transaction-handler.log<N>`; the bare name is
// only the template the worker suffixes, and never exists on a real host, so
// every numbered file is read. The events are JSON Lines and name their own host,
// so they route to a site from the event rather than from the file, are ordered
// by their own eventTime (the numbering is a rotation scheme, not a clock), and
// land in the same event ring with the same per-site counters. A host without the
// agent has no log directory, and that is not an error: it simply has no
// open-appsec events.
package wafmon
