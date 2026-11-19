// Package native holds the parts of a native build and its systemd unit that
// belong to no one program: the host-command runner, the unit status helpers,
// the builder-image match, the build archive extraction, the directory-backed
// build store and the Docker builder.
//
// Everything here is parameterised by a product label ("nginx", "php"), so an
// error still names the program the operator was looking at, and by the paths it
// works on, so no package here has to know another's layout.
package native
