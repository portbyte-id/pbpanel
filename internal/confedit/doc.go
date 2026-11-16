// Package confedit applies managed config changes: snapshot, atomic write,
// validate, reload, roll back, keep history.
//
// Every config file the panel manages is written through Editor.Apply and
// nowhere else (development plan §1.4). One apply is all-or-nothing across its
// files: either every file is in place and every service has validated,
// reloaded and reported healthy, or every file is back to the bytes it had
// before and the caller holds the validator's output.
//
// An apply also survives the daemon dying halfway. Before the first byte is
// written the previous state of every file goes into a journal on disk; the
// journal is removed when the apply ends either way. A journal found at boot
// means an apply never finished, and Recover puts those files back before
// anything reloads a service onto a config nobody validated.
package confedit
