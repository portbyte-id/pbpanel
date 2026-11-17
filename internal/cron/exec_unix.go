//go:build unix

package cron

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"time"
)

// OSExecutor runs commands as real processes. It starts each in its own process
// group, drops to the target account's uid, gid and supplementary groups when that
// differs from the panel's own, and kills the whole group when the context ends.
type OSExecutor struct{}

// killGrace is how long Wait may linger on output pipes after the group is killed.
const killGrace = 5 * time.Second

// Run implements Executor.
func (OSExecutor) Run(ctx context.Context, c Command) (int, error) {
	if len(c.Args) == 0 {
		return -1, errors.New("cron: empty argv")
	}
	cmd := exec.CommandContext(ctx, c.Path, c.Args[1:]...)
	cmd.Args = c.Args
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdout = c.Output
	cmd.Stderr = c.Output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if uint32(os.Geteuid()) != c.User.UID || uint32(os.Getegid()) != c.User.GID {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid:    c.User.UID,
			Gid:    c.User.GID,
			Groups: c.User.Groups,
		}
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = killGrace

	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, err
}

// LookupOSUser resolves an account from the system user database.
func LookupOSUser(name string) (Account, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return Account{}, err
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return Account{}, fmt.Errorf("cron: user %q has uid %q", name, u.Uid)
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return Account{}, fmt.Errorf("cron: user %q has gid %q", name, u.Gid)
	}
	acct := Account{Name: u.Username, UID: uint32(uid), GID: uint32(gid), Home: u.HomeDir}
	acct.Groups = []uint32{uint32(gid)}
	if ids, err := u.GroupIds(); err == nil {
		for _, s := range ids {
			if g, err := strconv.ParseUint(s, 10, 32); err == nil && uint32(g) != uint32(gid) {
				acct.Groups = append(acct.Groups, uint32(g))
			}
		}
	}
	return acct, nil
}
