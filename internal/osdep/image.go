package osdep

import "strings"

// ImageInstallCommand returns the shell command a Dockerfile RUN uses to install
// pkgs into a Debian-based image. It refreshes the package index, installs the
// packages, and removes the index so the layer stays small. extra words follow the
// packages verbatim, so callers pass constants or variables the image defines,
// never user input.
func ImageInstallCommand(pkgs []string, extra ...string) string {
	args := []string{"install", "-y", "--no-install-recommends"}
	args = append(args, pkgs...)
	args = append(args, extra...)
	return "apt-get update && apt-get " + strings.Join(args, " ") + " && rm -rf /var/lib/apt/lists/*"
}
