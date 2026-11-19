package nginx

import (
	"io"

	"github.com/portbyte-id/server-panel/internal/native"
)

// ExtractBuild unpacks the archive of a finished build into versionsDir/<id>. The
// tree is written beside the target and renamed into place, so a build is either
// there whole or not at all, and an existing build is never overwritten.
func ExtractBuild(r io.Reader, versionsDir, id string) error {
	return native.ExtractBuild(r, versionsDir, id, product, buildMarkers)
}

// BuildVersion is the nginx release a build holds, as the build recorded it.
func BuildVersion(p Paths, id string) (string, error) {
	return native.BuildVersion(p.VersionFile(id), id, product)
}
