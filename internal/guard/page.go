package guard

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"strings"
)

// The page is static per site: nginx serves it from disk with status 403, so a
// client that does not run JavaScript costs one file read. Its script and style
// are inline and fixed; the CSP header allows exactly them by hash.

const pageStyle = `
:root{color-scheme:light dark;--bg:#f7f7f8;--fg:#18181b;--muted:#5f5f6b;--card:#fff;--line:#e4e4e7;--accent:#2563eb}
@media (prefers-color-scheme:dark){:root{--bg:#0f0f12;--fg:#f4f4f5;--muted:#a1a1aa;--card:#18181b;--line:#2e2e33;--accent:#60a5fa}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:16px;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{width:100%;max-width:440px;background:var(--card);border:1px solid var(--line);border-radius:12px;padding:28px 24px}
h1{margin:0 0 4px;font-size:20px;font-weight:600;overflow-wrap:anywhere}
p{margin:0}
.muted{color:var(--muted);font-size:14px}
.status{margin-top:20px;display:flex;align-items:center;gap:10px;min-height:24px}
.spin{width:18px;height:18px;flex:none;border:2px solid var(--line);border-top-color:var(--accent);border-radius:50%;animation:s 0.8s linear infinite}
.done .spin{display:none}
@keyframes s{to{transform:rotate(360deg)}}
@media (prefers-reduced-motion:reduce){.spin{animation-duration:3s}}
#pbg-widget{margin-top:16px;min-height:0}
button{margin-top:16px;font:inherit;font-size:14px;padding:6px 14px;border-radius:8px;border:1px solid var(--line);background:transparent;color:var(--fg);cursor:pointer}
button:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
footer{margin-top:24px;padding-top:12px;border-top:1px solid var(--line)}
`

// pageScript solves the proof of work with a small synchronous SHA-256, since
// crypto.subtle exists only on HTTPS pages, or runs the Turnstile widget. The
// hashing yields to the browser every few thousand tries, so the page stays
// responsive and the spinner keeps turning.
const pageScript = `
(function(){"use strict";
var B=document.body,P=B.getAttribute("data-provider"),K=B.getAttribute("data-sitekey");
var S=document.getElementById("pbg-status"),T=document.getElementById("pbg-text"),R=document.getElementById("pbg-retry");
function say(t){T.textContent=t}
function fail(t){S.className="status done";say(t);R.hidden=false;R.focus()}
function reload(){location.replace(location.href)}
R.addEventListener("click",reload);
function post(body){
  return fetch("/.pb-challenge/verify",{method:"POST",credentials:"same-origin",cache:"no-store",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)})
  .then(function(r){
    if(r.status===204){say("Verified. Opening the site…");reload();return}
    return r.json().then(function(j){fail(j.detail||"The check did not pass.")},function(){fail("The check did not pass. Try again.")})
  },function(){fail("The server could not be reached. Try again.")});
}
var C=[1116352408,1899447441,3049323471,3921009573,961987163,1508970993,2453635748,2870763221,3624381080,310598401,607225278,1426881987,1925078388,2162078206,2614888103,3248222580,3835390401,4022224774,264347078,604807628,770255983,1249150122,1555081692,1996064986,2554220882,2821834349,2952996808,3210313671,3336571891,3584528711,113926993,338241895,666307205,773529912,1294757372,1396182291,1695183700,1986661051,2177026350,2456956037,2730485921,2820302411,3259730800,3345764771,3516065817,3600352804,4094571909,275423344,430227734,506948616,659060556,883997877,958139571,1322822218,1537002063,1747873779,1955562222,2024104815,2227730452,2361852424,2428436474,2756734187,3204031479,3329325298];
var W=new Array(64);
function sha(s){
  var n=s.length,l=((n+8)>>6)+1<<4,w=[],i,j,t;
  for(i=0;i<l;i++)w[i]=0;
  for(i=0;i<n;i++)w[i>>2]|=(s.charCodeAt(i)&255)<<(24-(i&3)*8);
  w[n>>2]|=128<<(24-(n&3)*8);w[l-1]=n*8;
  var H=[1779033703,3144134277,1013904242,2773480762,1359893119,2600822924,528734635,1541459225];
  for(j=0;j<l;j+=16){
    var a=H[0],b=H[1],c=H[2],d=H[3],e=H[4],f=H[5],g=H[6],h=H[7];
    for(t=0;t<64;t++){
      if(t<16)W[t]=w[j+t]|0;else{var x=W[t-15],y=W[t-2];W[t]=(((x>>>7|x<<25)^(x>>>18|x<<14)^(x>>>3))+W[t-7]+((y>>>17|y<<15)^(y>>>19|y<<13)^(y>>>10))+W[t-16])|0}
      var u=(h+((e>>>6|e<<26)^(e>>>11|e<<21)^(e>>>25|e<<7))+((e&f)^(~e&g))+C[t]+W[t])|0;
      var v=(((a>>>2|a<<30)^(a>>>13|a<<19)^(a>>>22|a<<10))+((a&b)^(a&c)^(b&c)))|0;
      h=g;g=f;f=e;e=(d+u)|0;d=c;c=b;b=a;a=(u+v)|0;
    }
    H[0]=H[0]+a|0;H[1]=H[1]+b|0;H[2]=H[2]+c|0;H[3]=H[3]+d|0;H[4]=H[4]+e|0;H[5]=H[5]+f|0;H[6]=H[6]+g|0;H[7]=H[7]+h|0;
  }
  return H;
}
function zeros(H,d){
  for(var i=0;i<8&&d>0;i++){
    if(d>=32){if(H[i]!==0)return false;d-=32}else return (H[i]>>>(32-d))===0;
  }
  return true;
}
function pow(){
  say("Checking your browser…");
  fetch("/.pb-challenge/new",{credentials:"same-origin",cache:"no-store"}).then(function(r){
    if(!r.ok)throw new Error();return r.json();
  }).then(function(j){
    var ch=j.challenge+":",d=j.difficulty,nonce=0;
    (function step(){
      for(var k=0;k<4000;k++,nonce++){
        if(zeros(sha(ch+nonce),d)){say("Checking the answer…");post({challenge:j.challenge,nonce:String(nonce)});return}
      }
      setTimeout(step,0);
    })();
  }).catch(function(){fail("The check could not start. Try again.")});
}
function turnstile(){
  say("Complete the check below.");
  window.pbgTurnstile=function(){
    window.turnstile.render("#pbg-widget",{sitekey:K,callback:function(t){say("Checking…");post({token:t})},
      "error-callback":function(){fail("The check could not load. Try again.")},
      "expired-callback":function(){fail("The check expired. Try again.")}});
  };
  var s=document.createElement("script");
  s.src="https://challenges.cloudflare.com/turnstile/v0/api.js?onload=pbgTurnstile&render=explicit";
  s.async=true;s.onerror=function(){fail("The check could not load. Check your connection and try again.")};
  document.head.appendChild(s);
}
if(P==="turnstile")turnstile();else pow();
})();
`

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>Checking your browser · {{.Domain}}</title>
<style>{{.Style}}</style>
</head>
<body data-provider="{{.Provider}}" data-sitekey="{{.SiteKey}}">
<main>
<h1>{{.Domain}}</h1>
<p class="muted">This site checks that visitors are people before letting them in. It takes a moment and happens once.</p>
<div id="pbg-status" class="status" role="status" aria-live="polite"><span class="spin" aria-hidden="true"></span><span id="pbg-text">Checking your browser…</span></div>
<noscript><p>Turn on JavaScript and reload the page to continue.</p></noscript>
<div id="pbg-widget"></div>
<button id="pbg-retry" type="button" hidden>Try again</button>
<footer><p class="muted">{{if eq .Provider "turnstile"}}The check is provided by Cloudflare Turnstile.{{else}}Your browser solves a small puzzle; nothing is sent to anyone else.{{end}}</p></footer>
</main>
<script>{{.Script}}</script>
</body>
</html>
`))

// Page renders a site's challenge page. siteKey is used by Turnstile only.
func Page(domain string, provider Provider, siteKey string) []byte {
	var b bytes.Buffer
	err := pageTmpl.Execute(&b, struct {
		Domain, SiteKey string
		Provider        Provider
		Style           template.CSS
		Script          template.JS
	}{domain, siteKey, provider, template.CSS(pageStyle), template.JS(pageScript)})
	if err != nil {
		// The template and its inputs are fixed shapes; a failure is a bug.
		panic(err)
	}
	return b.Bytes()
}

func cspHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// CSP is the Content-Security-Policy the page is served with: only its own
// script and style, the panel's two endpoints, and Cloudflare for Turnstile.
func CSP(provider Provider) string {
	script := []string{cspHash(pageScript)}
	frame := "'none'"
	if provider == ProviderTurnstile {
		script = append(script, "https://challenges.cloudflare.com")
		frame = "https://challenges.cloudflare.com"
	}
	return strings.Join([]string{
		"default-src 'none'",
		"script-src " + strings.Join(script, " "),
		"style-src " + cspHash(pageStyle),
		"connect-src 'self'",
		"frame-src " + frame,
		"img-src 'self' data:",
		"base-uri 'none'",
		"form-action 'none'",
		"frame-ancestors 'none'",
	}, "; ")
}
