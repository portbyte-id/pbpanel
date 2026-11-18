package nginx

import "bytes"

// landingLocation is the catch-all location of the default server: a small page for a
// host name that no site claims. The HTML holds no single quote, dollar sign or
// backslash, so it sits safely inside an nginx single-quoted string.
const landingLocation = `        location / {
            default_type text/html;
            return 200 '<!doctype html>
<html lang=en>
<meta charset=utf-8>
<meta name=viewport content=width=device-width,initial-scale=1>
<title>No website configured</title>
<style>
:root{color-scheme:light dark}
body{margin:0;min-height:100vh;display:grid;place-items:center;font:16px/1.5 system-ui,sans-serif;background:#fff;color:#111}
main{max-width:30rem;padding:2rem;text-align:center}
h1{font-size:1.25rem;margin:0 0 .5rem}
p{margin:0;color:#555}
@media(prefers-color-scheme:dark){body{background:#111;color:#eee}p{color:#aaa}}
</style>
<main>
<h1>No website is configured for this address</h1>
<p>Served by PB Panel | Copyright &copy; <script>document.write(new Date().getFullYear());</script> <a href="https://portbyte.id">PortByte Technologies</a></p>
</main>
';
        }`

// legacyLandingLocation is what earlier versions generated: drop the connection.
const legacyLandingLocation = `        location / {
            return 444;
        }`

// legacyMimeInclude is where the container image kept the media type table. A
// native build has no /etc/nginx; the table ships inside the build.
const legacyMimeInclude = "include /etc/nginx/mime.types;"

// legacyErrorLog sent nginx's own errors to the container's stderr, which Docker
// kept. A native nginx writes them to a file the log viewer reads.
const legacyErrorLog = "error_log /dev/stderr warn;"

// UpgradeMainConf brings a generated nginx.conf up to date: the old catch-all block
// becomes the landing page, the media type table is read from the build instead
// of the container image's /etc/nginx, and nginx's own errors go to a log file. The
// bool reports whether it changed anything. nginx.conf is operator-owned, so a part
// that no longer matches byte-for-byte is left as it is.
func UpgradeMainConf(content []byte, p Paths) ([]byte, bool) {
	out, changed := content, false
	if old := []byte(legacyErrorLog); bytes.Count(out, old) == 1 {
		out, changed = bytes.Replace(out, old, []byte("error_log "+p.MainErrorLog()+" warn;"), 1), true
	}
	if old := []byte(legacyLandingLocation); bytes.Count(out, old) == 1 {
		out, changed = bytes.Replace(out, old, []byte(landingLocation), 1), true
	}
	if old := []byte(legacyMimeInclude); bytes.Count(out, old) == 1 {
		out, changed = bytes.Replace(out, old, []byte("include "+p.MimeTypes()+";"), 1), true
	}
	return out, changed
}
