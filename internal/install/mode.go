package install

import (
	"io/fs"
	"os"
)

// modeOK reports whether an entry's mode is at least as restrictive as want on
// the permission bits.
//
// "At least as restrictive" is the comparison that matters: a directory the
// operator tightened further is fine, one they loosened is not. The owner must
// keep every bit that is wanted (a directory the panel cannot write is not
// "tighter", it is broken), and group and other may hold no bit that is not
// wanted. So 0700 satisfies 0700 and 0750 satisfies 0755, but 0755 does not
// satisfy 0700.
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
	const owner, others = fs.FileMode(0o700), fs.FileMode(0o077)
	return have.Perm()&owner&want.Perm() == want.Perm()&owner &&
		have.Perm()&others&^want.Perm() == 0 &&
		have&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) == 0
}

// modesMeaningful is true on platforms where the permission bits are real.
func modesMeaningful() bool { return os.PathSeparator == '/' }
