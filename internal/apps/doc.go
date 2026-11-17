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
// Status of the spike: this list comes from reading the upstream source at the
// pinned release. It has not been run against a live daemon from this repository.
// scripts/smoke/5-apps.sh is the first run that does that. The decoder in rpc.go
// accepts both a bare value and a one-member struct as the reply of the
// bool-returning calls, because the source does not settle which the codec emits.
package apps
