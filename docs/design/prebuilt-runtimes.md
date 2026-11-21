# Prebuilt runtimes — nginx from CI instead of the host

Status: **nginx only. Code and unit tests are done; the workflow has never run on GitHub and no
host has installed a package yet** (see §6). PHP is the same shape and comes next.

## 1. What and why

nginx is compiled from source (OpenSSL 3.5, ModSecurity, the Core Rule Set) in a throwaway Docker
container on the host, which takes many minutes and needs Docker. A GitHub Actions job can run the
**same recipe** once per distro release and architecture, publish the result, and let a host
download it instead. It is an option: **off by default**, and every way it can fail ends in the
ordinary local build.

## 2. How it works

```
CI:    cmd/mkruntime build  →  nginx-1.30.2-ubuntu24.04-amd64-<id>.tar.gz + .manifest.json
       cmd/mkruntime index  →  index.json              (rolling prerelease `runtimes`)

Host:  Images.Build → family asks Prebuilt.PickID → package fits?
         yes → download, check sha256, ExtractBuild into versions/<id>   (no Docker)
         no  → DockerBuilder compiles, as before
       then, either way: checkCandidate (nginx -t against the live config) → swap → rollback kept
```

- **`cmd/mkruntime`** (not shipped in the binary) runs `nginx.BuildSpec` through
  `native.DockerBuilder` on a builder image chosen from a synthetic `os-release`, so the package is
  compiled on the distro it is for, whatever the runner runs. `index` merges the manifests and names
  superseded assets (newest 2 per product/series/distro release/arch/root are kept).
- **`internal/native`** — `Source.Find` (pick) and `Source.Fetch` (download, verify, unpack),
  `PackBuild`, `BuildIndex`. `Fetch` ends in the **same `ExtractBuild`** a container-built tree goes
  through: no absolute names, no climbing, no special files, required markers present, atomic rename.
- **`nginx.Prebuilt`** wraps `DockerBuilder` and implements `IDPicker`. `images.FamilyOptions.ChooseID`
  lets a family build under an ID it was offered.

### 2.1 Why the host keeps the package's ID

nginx compiles its `--prefix` and `--modules-path` in, and the prefix contains the build ID
(`/www/panel/nginx/versions/<id>`), so that a candidate never loads the serving build's module. A
package is therefore only valid under the ID CI compiled in. The family adopts that ID (same
`YYYYMMDDHHMMSS-hex6` shape, so `CheckID` still passes). An ID the catalogue already holds is never
reused: asking for an update when the newest published build is already installed **compiles locally**
and says so in the job output.

### 2.2 When a package fits

All of these must hold, or the host compiles:

| Condition | Why |
|---|---|
| same distro **and** release (`ubuntu 24.04`) | the binary links the host's own glibc |
| same architecture | |
| same install root (`/www`) | conf, log, run and cache paths are compiled in |
| same **recipe ID** (`nginx.RecipeID()`) | a hash of the rendered Dockerfile and everything it pins; an older package may lack what the panel now expects |
| the version asked for | a series takes the highest published release; a full version is exact |

The recipe ID changes on any edit to `builder.Dockerfile`, the pinned versions or the path layout —
nobody has to remember to bump it. Packages published before such an edit stop fitting until the
workflow has run again (it triggers on those paths).

### 2.3 Fallback

Not finding a package, a missing or newer-layout index, a download error, a size or digest mismatch,
or an archive `ExtractBuild` rejects: each is reported in the job output and the host builds from
source **under the same ID**. A cancelled job is not retried.

## 3. Turning it on

Settings → **Runtimes** (or `pbp set`):

| Key | Default | |
|---|---|---|
| `runtimes.prebuilt` | `false` | read at the start of every build |
| `runtimes.url` | `https://github.com/portbyte-id/pbpanel/releases/download/runtimes` | https only (loopback http is allowed for tests); change it for a fork or mirror |

First publish: GitHub → Actions → **Runtimes** → *Run workflow* (it also runs weekly and when the
recipe changes). The repository must be **public** for hosts to download anonymously and for the
free arm64 runners.

## 4. What this does and does not protect

- **Does:** corruption and truncation (sha256 from the index, size checked); a hostile or malformed
  index (every field is shape-checked before it reaches a path or URL; `file` is a bare name);
  a hostile archive (`ExtractBuild`); plain-http downloads and redirects off https; a package that
  would not run on this host (the distro/arch/root/recipe match, then `nginx -t` as for any candidate).
- **Does not:** authenticate the publisher. The digest sits beside the package, so someone who can
  write to the release can replace both. The panel then runs the binary as root. This is why the
  setting is off by default. A next step is GitHub artifact attestations
  (`actions/attest-build-provenance`) verified on the host, or a signature key shipped in the binary.

## 5. Cost

10 jobs per run (5 distro releases × amd64/arm64), roughly 15–25 minutes each. It runs weekly, so
each week publishes a fresh package even when upstream has no new release; `-keep 2` bounds the
release's size.

## 6. Not proven

- The workflow has **not run on GitHub**. YAML parses; every step it calls is covered by unit tests
  except `docker build` itself and the `gh release` calls.
- The recipe has only been built on the Ubuntu 26.04 sandbox. **22.04, 24.04, Debian 12 and 13 are
  untested**; a distro that fails to build only costs its own job (`fail-fast: false`).
- No host has installed a downloaded package end to end. The unit tests cover selection, download,
  verification, unpacking, the family adopting the ID, and every fallback; a real run on a sandbox
  host with `runtimes.prebuilt` on is the remaining check.
- EL (Rocky/RHEL) is out of scope, as it is for the builder itself (M9).

## 7. PHP

PHP builds the same way (`php.Builder` over `native.DockerBuilder`) and its prefix also holds the
build ID. What differs: the extension set is chosen per host, so the likely shape is one **full**
build (all catalogue extensions as shared `.so`) with the host keeping only the `conf.d` lines for the
extensions it selected. `native.Source`, `PackBuild`, `BuildIndex` and `mkruntime` are already
product-neutral apart from the recipe.
