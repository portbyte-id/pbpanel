# The native PHP build. It runs on a builder image that matches the host (same
# distro and release), so the binaries link the host's own glibc, and it leaves a
# self-contained prefix: php-fpm, the php CLI, every requested extension as a
# shared .so, every shared library they need except the glibc core, and
# php.ini-production.
#
# The panel reads the prefix out of the finished image and the image is thrown
# away. The upper-case words between two at-signs are placeholders that
# builder.go fills in. Nothing here is a build argument: BuildKit invalidates
# every layer after an ARG whose value changes, even a layer that never reads it,
# so a changed extension list would recompile the core.
FROM ubuntu:26.04 AS build
ENV DEBIAN_FRONTEND=noninteractive

# The pool account exists on the host, not in the builder, but the build's own
# `php-fpm -t` check resolves `user = www`, so the builder carries it too, as the
# official image carries www-data. Nothing of this account reaches the prefix.
RUN groupadd -g 1500 www \
 && useradd -u 1500 -g www -M -d /nonexistent -s /usr/sbin/nologin www

# Builder packages. Every one of these is thrown away with the image: none of them
# reaches the prefix, which is the point of the build being native. `file` is not
# optional: phpize runs /usr/bin/file to work out the build host.
#
# An extension's own -dev packages are installed in that extension's layer, not
# here: a layer that names them would change with the extension list and recompile
# the core, which is the caching step 0 asked for.
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential autoconf bison re2c pkg-config \
      ca-certificates curl xz-utils patchelf file \
      libxml2-dev libsqlite3-dev libssl-dev libcurl4-openssl-dev libonig-dev \
      zlib1g-dev libreadline-dev libsodium-dev libargon2-dev libsystemd-dev \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /build

# A series (8.3) takes the newest patch php.net lists; a full version (8.3.28)
# pins one. The sha256 is read from the same JSON, keyed on the exact tarball
# name, so the download is checked against the value php.net publishes for that
# release.
RUN set -eux; \
    q=""; [ -n "$q" ] || q="8.3"; \
    json=$(curl -fsSL "https://www.php.net/releases/index.php?json&version=${q}"); \
    if [ -n "" ]; then rel=""; else \
      rel=$(printf '%s' "$json" | sed -n 's/.*"version":"\([0-9][0-9.]*\)".*/\1/p'); \
    fi; \
    test -n "$rel"; \
    want=$(printf '%s' "$json" | tr '}' '\n' | grep "php-${rel}\.tar\.xz" | sed -n 's/.*"sha256":"\([a-f0-9]*\)".*/\1/p'); \
    test -n "$want"; \
    curl -fsSL -o php.tar.xz "https://www.php.net/distributions/php-${rel}.tar.xz"; \
    echo "$want  php.tar.xz" | sha256sum -c -; \
    tar -xJf php.tar.xz; mv "php-${rel}" php; \
    printf '%s\n' "$rel" > /build/php-release; \
    printf '%s\n' "$want" > /build/php-sha256

# The prefix is the build's own directory, so extension_dir and the compiled-in
# scan dir point at the build that is loading them, and a candidate never loads
# the serving build's modules. PHP has no --with-ld-opt (that is nginx's flag):
# the RUNPATH goes in LDFLAGS.
#
# This also means the core layer names the build ID, so it is unique per build
# and the ~5 min compile is paid on every build, not just the first. Step 0
# measured a cached core only because it reused one BUILD_ID; see the step 2
# report. Making the core cacheable needs a fixed staging prefix plus a runtime
# extension_dir override, which trades away the isolation above.
WORKDIR /build/php
RUN set -eux; P="/www/panel/php/8.3/versions/20261119103000-a1b2c3"; multi=$(gcc -print-multiarch); \
    export LDFLAGS="-Wl,-rpath,\$ORIGIN/../lib"; \
    t0=$(date +%s); \
    ./configure \
      --prefix="$P" \
      --sysconfdir="$P/etc" \
      --with-config-file-path="$P/etc" \
      --with-config-file-scan-dir="$P/etc/conf.d" \
      --enable-option-checking=fatal \
      --with-mhash --with-pic \
      --enable-mbstring --enable-mysqlnd \
      --with-password-argon2 --with-sodium=shared \
      --with-pdo-sqlite=/usr --with-sqlite3=/usr \
      --with-curl --with-iconv --with-openssl --with-readline --with-zlib \
      --disable-phpdbg --with-pear \
      --with-libdir="lib/${multi}" \
      --disable-cgi \
      --enable-fpm --with-fpm-user="www" --with-fpm-group="www" \
      --with-fpm-systemd; \
    make -j"$(nproc)"; make install; \
    echo "TIMING core $(( $(date +%s) - t0 ))s"

# The official image enables sodium from a conf.d file because it is built
# --with-sodium=shared; without one the module exists on disk and never loads,
# and `php -m` would not match the container's list.
RUN set -eux; P="/www/panel/php/8.3/versions/20261119103000-a1b2c3"; mkdir -p "$P/etc/conf.d"; \
    echo "extension=sodium" > "$P/etc/conf.d/20-sodium.ini"

# Extensions as shared .so files, one layer each, which is what docker-php-ext-install
# did. The list is baked into this text rather than passed as a build argument:
# BuildKit invalidates every later layer when an ARG's value changes, so an
# argument would invalidate layers that never read it.
#
# This does not make the core compile shared between builds: the prefix above
# names the build ID, so the core layer is unique per build either way. See the
# step 2 report; the two cannot both hold, because one binary has one
# compiled-in prefix.
RUN set -eux; P="/www/panel/php/8.3/versions/20261119103000-a1b2c3"; export PATH="$P/bin:$PATH"; \
    ( cd ext/mysqli && phpize && ./configure --with-mysqli=mysqlnd && make -j"$(nproc)" && make install ); \
    echo "extension=mysqli" > "$P/etc/conf.d/30-mysqli.ini"
RUN set -eux; P="/www/panel/php/8.3/versions/20261119103000-a1b2c3"; export PATH="$P/bin:$PATH"; \
    ( cd ext/opcache && phpize && ./configure --enable-opcache && make -j"$(nproc)" && make install ); \
    echo "zend_extension=opcache" > "$P/etc/conf.d/10-opcache.ini"
RUN set -eux; P="/www/panel/php/8.3/versions/20261119103000-a1b2c3"; export PATH="$P/bin:$PATH"; \
    ( cd ext/pdo_mysql && phpize && ./configure --with-pdo-mysql=mysqlnd && make -j"$(nproc)" && make install ); \
    echo "extension=pdo_mysql" > "$P/etc/conf.d/30-pdo_mysql.ini"

# Bundle every shared library the binaries and extensions need, except the glibc
# core, which has to be the host's own. RUNPATH is not inherited by a library's
# own dependencies, so every bundled library gets $ORIGIN as its own, and an
# extension reaches the build's lib from its deep extension_dir. Only regular
# files are globbed: lib/php is a directory, and patchelf cannot take one.
RUN set -eux; P="/www/panel/php/8.3/versions/20261119103000-a1b2c3"; mkdir -p "$P/lib"; \
    for b in "$P/sbin/php-fpm" "$P/bin/php" "$P"/lib/php/extensions/*/*.so; do ldd "$b" 2>/dev/null || true; done \
      | awk '/=> \//{print $3}' | sort -u \
      | grep -vE '/(libc|libm|libmvec|libdl|libpthread|librt|libresolv|libutil|libanl|ld-linux[^/]*)\.so' \
      | xargs -r -I{} cp -L {} "$P/lib/"; \
    find "$P/lib" -maxdepth 1 -type f -print0 | xargs -0 -r patchelf --set-rpath '$ORIGIN'; \
    patchelf --set-rpath '$ORIGIN/../lib' "$P/sbin/php-fpm" "$P/bin/php"; \
    for m in "$P"/lib/php/extensions/*/*.so; do patchelf --set-rpath '$ORIGIN/../../..' "$m"; done

# Strip, keep php.ini-production (the panel copies it to php.ini once), drop what
# only a build needs.
RUN set -eux; P="/www/panel/php/8.3/versions/20261119103000-a1b2c3"; \
    strip --strip-unneeded "$P/sbin/php-fpm" "$P/bin/php" "$P"/lib/php/extensions/*/*.so "$P"/lib/*.so* || true; \
    cp /build/php/php.ini-production "$P/etc/php.ini-production"; \
    rm -rf "$P/include" "$P/php" "$P/man" "$P/lib/php/build" "$P/lib/php/test" "$P/lib/php/doc" \
           "$P/etc/php-fpm.conf.default" "$P/etc/php-fpm.d" "$P/etc/pear.conf" "$P/etc/pecl"; \
    find "$P" -name '*.a' -delete; \
    du -sh "$P"

# A build that does not do what the panel expects of it is not kept. The version
# it actually compiled is written beside the binary: a series build picks the
# newest patch at build time, so the answer is only known here.
RUN set -eux; P="/www/panel/php/8.3/versions/20261119103000-a1b2c3"; rel=$(cat /build/php-release); \
    "$P/bin/php" -v | head -1; \
    "$P/bin/php" -v | grep -q "PHP ${rel}"; \
    printf '%s\n' "$rel" > "$P/version"; \
    printf '%s\n' "$(cat /build/php-sha256)" > "$P/source-sha256"; \
    printf '[global]\nerror_log=/dev/stderr\ndaemonize=no\n[www]\nuser=%s\ngroup=%s\nlisten=/tmp/t.sock\npm=static\npm.max_children=1\n' \
      "www" "www" > /tmp/fpm.conf; \
    "$P/sbin/php-fpm" -t -y /tmp/fpm.conf; \
    for m in 'mysqli' 'Zend OPcache' 'pdo_mysql'; do "$P/bin/php" -m | grep -qxF "$m"; done; \
    ! ldd "$P/sbin/php-fpm" "$P/bin/php" "$P"/lib/php/extensions/*/*.so 2>&1 | grep -q "not found"
LABEL panel.php.build="20261119103000-a1b2c3" panel.php.version="8.3"

# The prefix is the whole build, and the panel reads it out of this stage by path.
FROM scratch AS out
COPY --from=build /www/panel/php/8.3/versions/20261119103000-a1b2c3 /www/panel/php/8.3/versions/20261119103000-a1b2c3
