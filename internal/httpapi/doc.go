// Package httpapi holds the router, the middleware chain, the handlers and the embedded SPA.
//
// Everything the package needs is passed in through Deps: the router, the
// middleware and every handler are methods on one unexported server value, so
// no handler reads a package-level variable and a test can build a complete
// server over a temp database without touching a global.
package httpapi
