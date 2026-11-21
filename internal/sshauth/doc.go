// Package sshauth turns the sshd systemd journal into a bounded list of SSH
// authentication attempts and a summary of them.
//
// The source is journald, not /var/log/auth.log: rsyslog is optional on Ubuntu
// and absent from a minimal EL install, while journald is what runs sshd on every
// supported host. A Monitor reads `journalctl -o json` for the units sshd runs
// under (sshd.service on EL, ssh.service on Debian/Ubuntu, and
// sshd-session.service on OpenSSH 9.8+, which split the per-connection process
// into its own unit) and parses the message text sshd writes.
//
// Every record carries the journal's own timestamp, so nothing here reads the
// clock except to bound the window.
package sshauth
