// Package install puts the panel on a host and replaces its binary.
//
// `panel install` is a fixed list of named steps (installSteps), each with a
// done-check, so a second run repairs whatever is missing and changes nothing
// else. The steps are the whole contract: they are asserted by name in the
// tests, and the order is the order they run in.
//
// Every system command goes through osdep.Runner and every distro decision goes
// through osdep.OS, so the package runs against a fake host on any OS.
package install
