// Package images owns the Docker images the panel builds for PHP and nginx: the
// catalogue of builds, the pointer to the build each family serves, and the
// steps that move a service from one build to another (development plan M8).
//
// A build is never changed once it exists, and nothing serves a build until
// it has passed its validator. A swap either completes, or the previous image
// is started again and the candidate is removed.
package images
