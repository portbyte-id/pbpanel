// Package apps runs Go binaries, Node.js apps and arbitrary commands as
// supervised processes (development plan M5).
//
// The panel does not supervise anything itself. An app is a row in the database
// plus a program file under /www/panel/supervisor/conf.d that a separate
// supervisord process reads, so the apps keep running while the panel restarts
// or upgrades. The panel talks to supervisord over its XML-RPC interface on a
// Unix socket that only root can open.
//
// # The supervisord RPC subset (step 5.1)
//
// The Go supervisord (github.com/ochinchina/supervisord, pinned in
// internal/install) implements the Python supervisor's XML-RPC namespace. The
// panel uses exactly these methods and nothing else:
//
//	supervisor.getState                  -> {statecode, statename}
//	supervisor.getAllProcessInfo         -> [ProcessInfo]
//	supervisor.getProcessInfo(name)      -> ProcessInfo
//	supervisor.reloadConfig              -> [[added, changed, removed]]
//	supervisor.addProcessGroup(name)     -> bool   (creates the program, does not start it)
//	supervisor.removeProcessGroup(name)  -> bool   (stops and removes it)
//	supervisor.startProcess(name, wait)  -> bool
//	supervisor.stopProcess(name, wait)   -> bool
//
// There is no `update` call. `supervisorctl update` is reloadConfig followed by
// addProcessGroup for the added and changed groups and removeProcessGroup for the
// removed ones, which is what the reloader in supervisor.go does. addProcessGroup
// replaces a changed program and leaves it stopped, so the reloader starts it
// again when it was running before, or when it is new and autostart is set.
//
// The `ctl` subcommand is not a fallback on a Unix socket: upstream's README says
// the socket is not supported for ctl, which needs [inet_http_server]. The panel
// does not open a TCP listener for a root-owned process manager, so a missing RPC
// call is a missing feature, not something to shell out for. Process logs are read
// from the log file the program file names, which needs no RPC at all.
//
// Status of the spike: the list comes from the upstream source at the pinned
// release and was confirmed against a live daemon by scripts/smoke/5-apps.sh.
// Two things only the live daemon showed: process states come back as "Running",
// not RUNNING, so they are upper-cased on decode; and the log is written to
// out.log.<timestamp>, never to the plain name the program file gives. The decoder
// in rpc.go still accepts both a bare value and a one-member struct for the
// bool-returning calls, because the source does not settle which the codec emits.
// An edited program is reported as "changed" and restarted, and a program that
// dies inside startsecs makes startProcess fail; both were confirmed live.
package apps
