// Package sysinfo samples host metrics and reports service status (development
// plan §9, M7 steps 7.1 and 7.2).
//
// Metrics. Parsers read the /proc files (stat, loadavg, meminfo, uptime, net/dev,
// mounts); a Sampler reads them through a Source on an interval, derives CPU and
// network rates from consecutive readings, keeps the last Capacity snapshots in a
// fixed-size ring and fans each one out to subscribers. There is no time-series
// database. statfs sits behind Source, so the package builds and its tests run on
// any host; HostSource.Disk only works on Linux.
//
// Services. Services lists and controls a fixed allowlist: the panel's systemd
// units, the Docker containers the caller names, and the managed apps. systemd is
// driven by `systemctl` argv through a Commander, containers through the Docker
// client's Engine API, apps through the app manager. A name that is not on the
// allowlist is refused before anything runs.
package sysinfo
