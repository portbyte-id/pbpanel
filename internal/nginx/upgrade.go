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
<p>Served by PB Panel</p>
</main>
';
        }`

// legacyLandingLocation is what earlier versions generated: drop the connection.
const legacyLandingLocation = `        location / {
            return 444;
        }`

// UpgradeMainConf swaps the old generated catch-all block for the landing page. The
// bool reports whether it changed anything. nginx.conf is operator-owned, so a file
// that no longer contains the old block byte-for-byte is returned untouched.
func UpgradeMainConf(content []byte) ([]byte, bool) {
	old := []byte(legacyLandingLocation)
	if bytes.Count(content, old) != 1 {
		return content, false
	}
	return bytes.Replace(content, old, []byte(landingLocation), 1), true
}
