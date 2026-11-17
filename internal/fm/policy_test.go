package fm

import (
	"errors"
	"testing"
)

func TestPolicyClean(t *testing.T) {
	p := NewPolicy("/www/panel")
	tests := []struct {
		in      string
		want    string
		invalid bool
	}{
		{in: "/etc", want: "/etc"},
		{in: "/etc/", want: "/etc"},
		{in: "//etc", want: "/etc"},
		{in: "/etc/..", want: "/"},
		{in: "/a/./b//c", want: "/a/b/c"},
		{in: "/a/../../etc", want: "/etc"},
		{in: "", invalid: true},
		{in: "etc", invalid: true},
		{in: "../etc", invalid: true},
		{in: "/a\x00b", invalid: true},
		{in: "/a\xffb", invalid: true},
	}
	for _, tc := range tests {
		got, err := p.Clean(tc.in)
		if tc.invalid {
			if !errors.Is(err, ErrInvalidPath) {
				t.Errorf("Clean(%q) = %q, %v; want ErrInvalidPath", tc.in, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("Clean(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestPolicyDestructive(t *testing.T) {
	p := NewPolicy("/www/panel")
	denied := []string{
		"/", "/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/var",
		"/www/panel", "/www", "/root", "/home", "/proc", "/sys", "/dev",
		"/proc/1", "/sys/kernel", "/dev/null",
	}
	for _, d := range denied {
		if err := p.CheckDestructive(d); !errors.Is(err, ErrProtected) {
			t.Errorf("CheckDestructive(%q) = %v; want ErrProtected", d, err)
		}
	}
	// "/etc/.." and "//etc" only reach the policy after Clean, which is the
	// contract: the table above is what Clean produces for them.
	for _, in := range []string{"/etc/", "/usr/../etc", "//etc", "/etc/../etc"} {
		clean, err := p.Clean(in)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.CheckDestructive(clean); !errors.Is(err, ErrProtected) {
			t.Errorf("CheckDestructive(Clean(%q)=%q) = %v; want ErrProtected", in, clean, err)
		}
	}
	clean, _ := p.Clean("/etc/..")
	if err := p.CheckDestructive(clean); !errors.Is(err, ErrProtected) {
		t.Errorf("/etc/.. is the filesystem root and must be refused, got %v", err)
	}
	allowed := []string{
		"/etc/hosts", "/etc/nginx/conf.d", "/usr/local/bin/tool", "/var/log/syslog",
		"/www/panel/data/x", "/www/services", "/www/services/sites/a.test",
		"/tmp", "/tmp/x", "/home/user", "/etcetera", "/rootkit",
	}
	for _, a := range allowed {
		if err := p.CheckDestructive(a); err != nil {
			t.Errorf("CheckDestructive(%q) = %v; want allowed", a, err)
		}
	}
}

func TestPolicyWrite(t *testing.T) {
	p := NewPolicy("/www/panel")
	for _, d := range []string{"/proc", "/proc/sys/kernel/x", "/sys/a", "/dev", "/dev/shm/x"} {
		if err := p.CheckWrite(d); !errors.Is(err, ErrProtected) {
			t.Errorf("CheckWrite(%q) = %v; want ErrProtected", d, err)
		}
	}
	for _, d := range []string{"/etc/hosts", "/procfoo", "/devices/x", "/", "/tmp/x"} {
		if err := p.CheckWrite(d); err != nil {
			t.Errorf("CheckWrite(%q) = %v; want allowed", d, err)
		}
	}
}

func TestPolicyConfirm(t *testing.T) {
	p := NewPolicy("/www/panel")
	if err := p.CheckConfirm("/tmp/build", "build"); err != nil {
		t.Errorf("matching confirm: %v", err)
	}
	for _, c := range []string{"", "Build", "/tmp/build", "build "} {
		if err := p.CheckConfirm("/tmp/build", c); !errors.Is(err, ErrConfirm) {
			t.Errorf("CheckConfirm(%q) = %v; want ErrConfirm", c, err)
		}
	}
}

func TestCheckName(t *testing.T) {
	for _, n := range []string{"a", "a b", ".hidden", "ünï.txt"} {
		if err := CheckName(n); err != nil {
			t.Errorf("CheckName(%q) = %v", n, err)
		}
	}
	for _, n := range []string{"", ".", "..", "a/b", "a\x00", "\xff"} {
		if err := CheckName(n); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("CheckName(%q) = %v; want ErrInvalidPath", n, err)
		}
	}
}

func TestWithin(t *testing.T) {
	if !Within("/a", "/a") || !Within("/a", "/a/b") || !Within("/", "/x") {
		t.Error("expected containment")
	}
	if Within("/a", "/ab") || Within("/a/b", "/a") {
		t.Error("unexpected containment")
	}
}
