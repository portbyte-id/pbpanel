package apps

import "path"

// Paths is where the supervisor's files and the apps' files live. Like
// nginx.Paths it is built with package path, never filepath: the strings are
// written into config files read on Linux, whatever the daemon was built on.
type Paths struct {
	// Root is the install root, `/www` in production.
	Root string
}

// SupervisorDir holds supervisord.conf, its socket and its pid file.
func (p Paths) SupervisorDir() string { return path.Join(p.Root, "panel", "supervisor") }

// ConfDir holds one program file per app. It is the one directory the config
// editor may write for M5.
func (p Paths) ConfDir() string { return path.Join(p.SupervisorDir(), "conf.d") }

// ConfFile is an app's program file.
func (p Paths) ConfFile(name string) string { return path.Join(p.ConfDir(), name+".conf") }

// Socket is supervisord's XML-RPC socket, mode 0700 and owned by root.
func (p Paths) Socket() string { return path.Join(p.SupervisorDir(), "supervisord.sock") }

// LogDir is an app's log directory.
func (p Paths) LogDir(name string) string { return path.Join(p.Root, "panel", "logs", "apps", name) }

// LogFile is the file an app's stdout and stderr are written to.
func (p Paths) LogFile(name string) string { return path.Join(p.LogDir(name), "out.log") }

// AppsRoot is the directory whose subdirectories are the apps' default working
// directories. It is the sites tree, so an app's working directory sits with the
// web roots a site serves rather than in a separate apps tree.
func (p Paths) AppsRoot() string { return path.Join(p.Root, "services", "sites") }

// DefaultWorkDir is the working directory an app gets when none is given.
func (p Paths) DefaultWorkDir(name string) string { return path.Join(p.AppsRoot(), name) }
