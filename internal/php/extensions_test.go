package php

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/osdep"
)

func TestParseExtensionsAcceptsCatalogueAndPECLNames(t *testing.T) {
	got, err := ParseExtensions([]string{"redis", " intl ", "intl", "apcu"})
	if err != nil {
		t.Fatalf("ParseExtensions = %v", err)
	}
	if names := ExtensionNames(got); !slices.Equal(names, []string{"apcu", "intl", "redis"}) {
		t.Errorf("names = %v, want the unique names sorted", names)
	}
	for _, e := range got {
		if e.Name == "redis" && e.Kind != "pecl" {
			t.Errorf("redis kind = %q, want pecl", e.Kind)
		}
		if e.Name == "intl" && (e.Kind != "bundled" || e.Module != "intl") {
			t.Errorf("intl = %+v", e)
		}
	}
}

// A name in a RUN line is shell. Anything that is not a plain package name is
// refused before it can reach the Dockerfile.
func TestParseExtensionsRefusesWhatIsNotAPackageName(t *testing.T) {
	for _, bad := range []string{"x;rm -rf /", "$(id)", "../etc", "Redis", "a b", "redis-6.0.2", "", "r`id`", "x|y", "a\nb"} {
		_, err := ParseExtensions([]string{bad})
		var in *InputError
		if !errors.As(err, &in) || in.Field != "extensions" {
			t.Errorf("ParseExtensions(%q) = %v, want an extensions input error", bad, err)
		}
	}
}

func TestParseExtensionsCapsTheCount(t *testing.T) {
	var names []string
	for i := range MaxExtensions + 1 {
		names = append(names, "pkg"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+strings.Repeat("y", i/26))
	}
	if _, err := ParseExtensions(names); err == nil {
		t.Errorf("%d extensions were accepted; the cap is %d", len(names), MaxExtensions)
	}
}

func TestDockerfileForBundledAndPECLExtensions(t *testing.T) {
	exts, err := ParseExtensions([]string{"intl", "redis"})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"FROM php:8.3-fpm",
		"RUN groupadd -g 1500 www && useradd -u 1500 -g www -M -d /nonexistent -s /usr/sbin/nologin www",
		"RUN " + osdep.ImageInstallCommand([]string{"libicu-dev"}, "${PHPIZE_DEPS}"),
		"RUN docker-php-ext-install -j$(nproc) intl",
		"RUN pecl install redis && docker-php-ext-enable redis",
		"",
	}, "\n")
	if got := string(Dockerfile("8.3", exts)); got != want {
		t.Errorf("Dockerfile:\n--- want\n%s--- got\n%s", want, got)
	}
}

func TestDockerfileConfiguresGDAndSortsPackages(t *testing.T) {
	exts, _ := ParseExtensions([]string{"intl", "gd"})
	got := string(Dockerfile("8.4", exts))
	for _, line := range []string{
		"RUN " + osdep.ImageInstallCommand([]string{"libfreetype-dev", "libicu-dev", "libjpeg-dev", "libpng-dev", "libwebp-dev"}),
		"RUN docker-php-ext-configure gd --with-freetype --with-jpeg --with-webp",
		"RUN docker-php-ext-install -j$(nproc) gd intl",
	} {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("Dockerfile lacks %q:\n%s", line, got)
		}
	}
	if strings.Contains(got, "PHPIZE_DEPS") {
		t.Error("a build with no PECL extension installs the PECL build tools")
	}
}

func TestDockerfileLDAPLibdirIsAShellExpansionOfAConstant(t *testing.T) {
	exts, _ := ParseExtensions([]string{"ldap"})
	want := `RUN docker-php-ext-configure ldap --with-libdir="lib/$(uname -m)-linux-gnu"`
	if !strings.Contains(string(Dockerfile("8.3", exts)), want) {
		t.Errorf("ldap configure line is wrong:\n%s", Dockerfile("8.3", exts))
	}
}

func TestDockerfileWithNoExtensionsOnlyAddsTheWWWAccount(t *testing.T) {
	got := string(Dockerfile("8.5", nil))
	if strings.Count(got, "\n") != 2 || !strings.HasPrefix(got, "FROM php:8.5-fpm\n") {
		t.Errorf("Dockerfile = %q", got)
	}
}

func TestMissingModulesReadsPHPMOutput(t *testing.T) {
	out := "[PHP Modules]\nCore\nintl\nZend OPcache\n\n[Zend Modules]\nZend OPcache\n"
	exts, _ := ParseExtensions([]string{"intl", "opcache", "redis"})
	if got := missingModules(out, exts); !slices.Equal(got, []string{"redis"}) {
		t.Errorf("missing = %v, want [redis]", got)
	}
}

func TestCatalogueNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Catalogue() {
		if seen[e.Name] {
			t.Errorf("catalogue lists %s twice", e.Name)
		}
		seen[e.Name] = true
		if e.Kind != "bundled" && e.Kind != "pecl" {
			t.Errorf("%s has kind %q", e.Name, e.Kind)
		}
	}
}
