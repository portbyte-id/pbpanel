package nginx

import (
	"strings"
	"testing"
)

func TestPQCVersions(t *testing.T) {
	for _, v := range []string{"1.30", "pqc-1.30", "pqc-1.29"} {
		if err := CheckVersion(v); err != nil {
			t.Errorf("CheckVersion(%q) = %v", v, err)
		}
	}
	for _, v := range []string{"pqc-", "pqc1.30", "pqc-latest", "x-1.30"} {
		if CheckVersion(v) == nil {
			t.Errorf("CheckVersion(%q) accepted", v)
		}
	}
	if got := BaseImageFor("pqc-1.30"); !strings.HasPrefix(got, "alpine:") {
		t.Errorf("PQC base = %q, want Alpine", got)
	}
	if got := BaseImageFor("1.30"); got != "nginx:1.30" {
		t.Errorf("official base = %q", got)
	}
}

func TestPQCDockerfile(t *testing.T) {
	df := string(DockerfileFor("pqc-1.30"))
	for _, want := range []string{"FROM alpine:", "ARG NGINX_SERIES=1.30", "OPENSSL_SERIES=3.5", "sha256sum -c", "--with-openssl=", "adduser -D -H -u 1500", "addgroup -g", "MODSEC_VERSION=3.0.14", "--add-dynamic-module=/build/modsecurity-nginx", "ngx_http_modsecurity_module.so", "libmodsecurity.so"} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
	if strings.Contains(df, "%!") {
		t.Error("Dockerfile has a formatting error")
	}
	if off := string(DockerfileFor("1.30")); !strings.Contains(off, "FROM nginx:1.30") {
		t.Errorf("official Dockerfile changed: %s", off)
	}
}
