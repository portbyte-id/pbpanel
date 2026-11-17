// Package logs is the log viewer (registry, tail, search) and the panel-owned
// log rotation.
//
// Callers never hand this package a path. A Registry resolves an opaque source
// id to a Source that a trusted Provider supplied, and every read goes through
// that Source, so a request cannot name a file the panel did not publish.
package logs
