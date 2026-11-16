// Command panel is the server panel: one binary with a daemon role, install and
// upgrade, and a root-only recovery CLI.
package main

import (
	"context"
	"os"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
