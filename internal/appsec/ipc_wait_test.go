package appsec

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

// socketInfo is a stat result for a Unix socket, which is what the agent creates.
type socketInfo struct{ os.FileInfo }

func (socketInfo) Mode() os.FileMode { return os.ModeSocket | 0o666 }

// The agent is active before its IPC socket exists. The wait must hold the nginx
// reload until the socket appears, not return on the unit being active.
func TestWaitAgentIPCWaitsForTheSocket(t *testing.T) {
	calls := 0
	m := &Manager{ipcTimeout: time.Second, ipcPoll: time.Millisecond, stat: func(p string) (os.FileInfo, error) {
		if p != agentIPCSocket {
			t.Fatalf("stat %q, want %q", p, agentIPCSocket)
		}
		calls++
		if calls < 3 {
			return nil, fs.ErrNotExist
		}
		return socketInfo{}, nil
	}}
	if err := m.waitAgentIPC(context.Background(), func(string) {}); err != nil {
		t.Fatalf("waitAgentIPC: %v", err)
	}
	if calls != 3 {
		t.Errorf("stat called %d times, want 3", calls)
	}
}

// A regular file at the path is not the agent's socket, and a missing socket after
// the timeout fails the install rather than reloading nginx into nothing.
func TestWaitAgentIPCFailsWhenTheSocketNeverAppears(t *testing.T) {
	m := &Manager{ipcTimeout: 20 * time.Millisecond, ipcPoll: time.Millisecond, stat: func(string) (os.FileInfo, error) {
		return nil, fs.ErrNotExist
	}}
	err := m.waitAgentIPC(context.Background(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), agentIPCSocket) {
		t.Fatalf("err = %v, want a refusal naming %s", err, agentIPCSocket)
	}
}
