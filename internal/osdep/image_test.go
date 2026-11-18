package osdep

import "testing"

// The exact image command is pinned here. Callers outside osdep build their
// Dockerfile lines through ImageInstallCommand and do not repeat the literal.
func TestImageInstallCommandPinsTheDebianLine(t *testing.T) {
	cases := []struct {
		name  string
		pkgs  []string
		extra []string
		want  string
	}{
		{
			name:  "packages and PECL build tools",
			pkgs:  []string{"libicu-dev"},
			extra: []string{"${PHPIZE_DEPS}"},
			want:  "apt-get update && apt-get install -y --no-install-recommends libicu-dev ${PHPIZE_DEPS} && rm -rf /var/lib/apt/lists/*",
		},
		{
			name: "packages only",
			pkgs: []string{"libfreetype-dev", "libicu-dev", "libjpeg-dev", "libpng-dev", "libwebp-dev"},
			want: "apt-get update && apt-get install -y --no-install-recommends libfreetype-dev libicu-dev libjpeg-dev libpng-dev libwebp-dev && rm -rf /var/lib/apt/lists/*",
		},
		{
			name:  "PECL build tools only",
			extra: []string{"${PHPIZE_DEPS}"},
			want:  "apt-get update && apt-get install -y --no-install-recommends ${PHPIZE_DEPS} && rm -rf /var/lib/apt/lists/*",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ImageInstallCommand(c.pkgs, c.extra...); got != c.want {
				t.Errorf("ImageInstallCommand = %q, want %q", got, c.want)
			}
		})
	}
}
