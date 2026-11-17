//go:build !unix

package cron

import (
	"context"
	"errors"
)

var errUnsupported = errors.New("cron: running jobs as another user is supported on Linux only")

// OSExecutor is a stub off Unix: the panel manages Linux hosts, and credential
// dropping has no portable form. The package still builds so the rest of the
// repository can be compiled and tested on a developer machine.
type OSExecutor struct{}

// Run implements Executor.
func (OSExecutor) Run(context.Context, Command) (int, error) { return -1, errUnsupported }

// LookupOSUser always fails off Unix.
func LookupOSUser(string) (Account, error) { return Account{}, errUnsupported }
