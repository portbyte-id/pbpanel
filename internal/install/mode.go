package install

import (
	"io/fs"
	"os"
)

// modeOK reports whether an entry's mode is at least as restrictive as want on
// the permission bits.
//
// "At least as restrictive" is the comparison that matters: a directory the
// operator tightened further is fine, one they loosened is not. Owner, group
// and other are compared per class, so 0750 satisfies 0700 but 0755 does not.
//
// On Windows the permission bits are not meaningful and os.FileMode carries
// only the read-only attribute; there the mode check is skipped. That is the
// same split as the *_unix_test.go convention in development plan §1.4: file
// modes are proven on Linux, and a Windows "pass" proves nothing.
func modeOK(have, want fs.FileMode) bool {
	if !modesMeaningful() {
		return true
	}
	// Special bits (setuid, setgid, sticky) are compared too: a setgid
	// directory under the panel tree is a change install did not make.
	return have.Perm()&want.Perm() == want.Perm() &&
		have&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) == 0
}

// modesMeaningful is true on platforms where the permission bits are real.
func modesMeaningful() bool { return os.PathSeparator == '/' }
