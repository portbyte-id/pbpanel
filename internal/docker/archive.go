package docker

import (
	"context"
	"io"
	"net/http"
	"net/url"
)

// ContainerArchive streams a tar archive of path inside a container, which need
// not be running. The archive holds the path's own base name as its top entry
// (`path/to/dir` comes back as `dir/...`). The caller closes the reader.
//
// It is how the native nginx build leaves the builder image: the image is made
// into a stopped container and the finished prefix is read out of it.
func (c *Client) ContainerArchive(ctx context.Context, id, path string) (io.ReadCloser, error) {
	if err := checkName("container", id); err != nil {
		return nil, err
	}
	q := url.Values{"path": {path}}
	resp, err := c.do(ctx, request{op: "archive " + path + " of container " + id, method: http.MethodGet, path: "/containers/" + id + "/archive", query: q})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
