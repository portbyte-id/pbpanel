# The native nginx build. It runs on a builder image that matches the host (same
# distro and release), so the binary links the host's own glibc, and it leaves a
# self-contained prefix: nginx, the ModSecurity module, the open-appsec
# attachment, every shared library they need except the glibc core, the Core Rule
# Set and mime.types.
#
# The panel reads the prefix out of the finished image and the image is thrown
# away. The upper-case words between two at-signs are placeholders that
# builder.go fills in.
FROM @BASE@
ENV DEBIAN_FRONTEND=noninteractive
# cmake, libbrotli-dev, python3 and dos2unix are for the open-appsec attachment:
# it is a CMake project that links brotli and shells out to python3 while it
# configures, and the vendor's nginx version script calls dos2unix.
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential libpcre2-dev zlib1g-dev perl curl ca-certificates \
      libxml2-dev libyajl-dev liblmdb-dev pkg-config patchelf \
      cmake libbrotli-dev python3 dos2unix \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /build

# libmodsecurity (ModSecurity v3) and its nginx connector, from the release
# tarballs, each checked against the digest published beside it.
RUN set -eux; \
    for f in "modsecurity|modsecurity-v@MODSEC_VERSION@.tar.gz|https://github.com/owasp-modsecurity/ModSecurity/releases/download/v@MODSEC_VERSION@" \
             "modsecurity-nginx|modsecurity-nginx-v@MODSEC_NGINX_VERSION@.tar.gz|https://github.com/owasp-modsecurity/ModSecurity-nginx/releases/download/v@MODSEC_NGINX_VERSION@"; do \
      dir=${f%%|*}; rest=${f#*|}; name=${rest%%|*}; base=${rest#*|}; \
      curl -fsSLO "$base/$name"; \
      want=$(curl -fsSL "$base/$name.sha256" | cut -d' ' -f1); \
      echo "$want  $name" | sha256sum -c -; \
      mkdir "$dir"; tar -xzf "$name" -C "$dir" --strip-components=1; \
    done

# libmodsecurity goes to a private staging prefix, never /usr: it is copied into
# the build's own lib directory below. Remote rules (libcurl) are not built; the
# firewall never loads rules from a URL, and curl drags in a TLS stack.
RUN cd modsecurity \
 && ./configure --prefix=/opt/msc --with-pcre2 --with-lmdb --without-curl --without-lua --disable-examples --disable-doxygen-doc \
 && make -j"$(nproc)" \
 && make install

# The OWASP Core Rule Set, pinned and checked against the published digest.
RUN set -eux; \
    curl -fsSLO "https://github.com/coreruleset/coreruleset/releases/download/v@CRS_VERSION@/coreruleset-@CRS_VERSION@-minimal.tar.gz"; \
    echo "@CRS_SHA256@  coreruleset-@CRS_VERSION@-minimal.tar.gz" | sha256sum -c -; \
    mkdir crs; tar -xzf "coreruleset-@CRS_VERSION@-minimal.tar.gz" -C crs --strip-components=1; \
    test -d crs/rules

# A series (1.30) takes the newest patch release at build time, so an update is a
# rebuild that picks up fixes; a full version (1.30.1) pins that one release. The
# OpenSSL tarball is checked against the digest published beside it.
RUN set -eux; \
    if [ -n "@NGINX_EXACT@" ]; then \
      nginx_tgz="nginx-@NGINX_EXACT@.tar.gz"; \
    else \
      nginx_tgz=$(curl -fsSL https://nginx.org/download/ | grep -oE "nginx-@NGINX_SERIES_RE@\.[0-9]+\.tar\.gz" | sort -uV | tail -n1); \
    fi; \
    test -n "$nginx_tgz"; \
    curl -fsSL "https://nginx.org/download/$nginx_tgz" | tar -xz; \
    mv "${nginx_tgz%.tar.gz}" nginx; \
    ossl_tgz=$(curl -fsSL https://www.openssl.org/source/ | grep -oE "openssl-@OPENSSL_SERIES@\.[0-9]+\.tar\.gz" | sort -uV | tail -n1); \
    test -n "$ossl_tgz"; \
    curl -fsSLO "https://www.openssl.org/source/$ossl_tgz"; \
    want=$(curl -fsSL "https://www.openssl.org/source/$ossl_tgz.sha256" | cut -d' ' -f1); \
    echo "$want  $ossl_tgz" | sha256sum -c -; \
    tar -xzf "$ossl_tgz"; \
    mv "${ossl_tgz%.tar.gz}" openssl

WORKDIR /build/nginx
ENV MODSECURITY_INC=/opt/msc/include MODSECURITY_LIB=/opt/msc/lib
# The prefix is the build's own directory, so `load_module modules/...` in a
# configuration resolves against the build that loads it, candidate or serving.
RUN set -eux; P="@PREFIX@"; \
    ./configure \
      --prefix="$P" --sbin-path="$P/sbin/nginx" --modules-path="$P/modules" \
      --conf-path="@CONF_PATH@" \
      --error-log-path="@LOG_DIR@/nginx-error.log" --http-log-path="@LOG_DIR@/nginx-access.log" \
      --pid-path="@RUN_DIR@/nginx.pid" --lock-path="@RUN_DIR@/nginx.lock" \
      --http-client-body-temp-path="@CACHE_DIR@/client_temp" \
      --http-proxy-temp-path="@CACHE_DIR@/proxy_temp" \
      --http-fastcgi-temp-path="@CACHE_DIR@/fastcgi_temp" \
      --http-uwsgi-temp-path="@CACHE_DIR@/uwsgi_temp" \
      --http-scgi-temp-path="@CACHE_DIR@/scgi_temp" \
      --user=@WWW_USER@ --group=@WWW_USER@ \
      --add-dynamic-module=/build/modsecurity-nginx \
      --with-openssl=/build/openssl --with-openssl-opt="no-tests" \
      --with-cc-opt="-O2" --with-ld-opt="-Wl,-rpath,\$ORIGIN/../lib" \
      --with-compat --with-threads --with-file-aio \
      --with-http_ssl_module --with-http_v2_module --with-http_v3_module \
      --with-http_realip_module --with-http_gzip_static_module --with-http_gunzip_module \
      --with-http_slice_module --with-http_stub_status_module --with-http_auth_request_module \
      --with-http_secure_link_module --with-http_sub_module --with-http_addition_module \
      --with-stream --with-stream_ssl_module --with-stream_ssl_preread_module --with-stream_realip_module; \
    make -j"$(nproc)"; make install

# The open-appsec attachment, an nginx dynamic module built from its own
# repository. That repository has no tags and no releases, so the commit itself is
# pinned, and the tarball GitHub makes of it is checked against a digest recorded
# here: the same commit always produces the same archive.
#
# The file is named with -o rather than -O: GitHub's last path segment is the bare
# commit, while the archive it serves unpacks into attachment-<commit>/, and one
# name for both keeps the check, the unpack and the digest line reading alike.
RUN set -eux; \
    tgz="attachment-@OPENAPPSEC_ATTACHMENT_SHA@.tar.gz"; \
    curl -fsSL -o "$tgz" "https://github.com/openappsec/attachment/archive/@OPENAPPSEC_ATTACHMENT_SHA@.tar.gz"; \
    echo "@OPENAPPSEC_ATTACHMENT_SHA256@  $tgz" | sha256sum -c -; \
    tar -xzf "$tgz"; \
    mv "attachment-@OPENAPPSEC_ATTACHMENT_SHA@" /build/attachment

# It is compiled against a *configured* nginx tree: its CMakeLists reads
# nginx-src/include_paths.mk and nginx-src/cc_flags.mk out of the install prefix.
# The vendor generates those by downloading nginx again and re-running configure;
# here they come from the tree just built, so the module sees this build's include
# paths and flags and not a second, unrelated set. -DCMAKE_POLICY_VERSION_MINIMUM
# is required because the project still asks for cmake_minimum_required(2.8.4),
# which CMake 4 refuses.
RUN set -eux; \
    cd /build/nginx; \
    awk '/^ALL_INCS = /{f=1} f{print} /^$/ && f{exit}' objs/Makefile \
      | tr '\\' ' ' | sed 's/ALL_INCS =//; s/-I//g' | tr -s ' ' '\n' | sed '/^$/d' \
      > include_paths.mk; \
    test -s include_paths.mk; \
    echo "-O2" > cc_flags.mk; \
    mkdir -p /build/attach-install; cp -a /build/nginx /build/attach-install/nginx-src; \
    cp include_paths.mk cc_flags.mk /build/attach-install/nginx-src/; \
    mkdir /build/attach-build; cd /build/attach-build; \
    cmake -DCMAKE_INSTALL_PREFIX=/build/attach-install -DATTACHMENT_TYPE=nginx \
          -DCMAKE_BUILD_TYPE=Release -DCMAKE_POLICY_VERSION_MINIMUM=3.5 /build/attachment; \
    make -j"$(nproc)"; make install

# The module is shipped under the name the panel's configurations load, which is
# the name the vendor's own installer renames it to. Its six libraries go to the
# build's lib directory; the module's NEEDED names three of them, and the other
# three are kept because the vendor package ships the same six and the agent side
# uses them.
#
# lib/ is made here: nginx's own install creates modules/ and conf/, but not lib/,
# which only the bundling step below would have made.
RUN set -eux; P="@PREFIX@"; \
    L=/build/attach-install/nginx_attachment/lib; \
    mkdir -p "$P/lib"; \
    install -m 0755 "$L/libngx_module.so" "$P/modules/ngx_cp_attachment_module.so"; \
    install -m 0755 "$L/libshmem_ipc_2.so" "$L/libosrc_shmem_ipc.so" \
        "$L/libosrc_compression_utils.so" "$L/libosrc_nginx_attachment_util.so" \
        "$L/libnano_attachment.so" "$L/libnano_attachment_util.so" "$P/lib/"

# Bundle every shared library the binary and the module need, except the glibc
# core, which has to be the host's own. RUNPATH is not inherited by a library's
# dependencies, so every bundled library gets $ORIGIN as its own.
#
# $P/lib is on the search path of the walk below, because the attachment's
# libraries are already in it: without that, the walk would not see the C++ runtime
# they pull in (libstdc++, libgcc_s) and the build would ship a module that cannot
# load. A library the walk resolves to its own place in $P/lib is already bundled
# and is left alone; copying it onto itself is an error.
#
# The variable is scoped to that walk and unset before patchelf runs. patchelf is a
# C++ program, so with $P/lib on the path it loads a libstdc++/libgcc_s that it is
# about to rewrite, mapping the file it writes into its own address space: it then
# dies of SIGSEGV on exactly those two libraries, and the build fails with a 139
# that says nothing about why.
RUN set -eux; P="@PREFIX@"; mkdir -p "$P/lib" "$P/conf"; \
    { export LD_LIBRARY_PATH=/opt/msc/lib:"$P/lib"; \
      for b in "$P/sbin/nginx" "$P"/modules/*.so; do ldd "$b"; done; } \
      | awk '/=> \//{print $3}' | sort -u \
      | grep -vF "$P/lib/" \
      | grep -vE '/(libc|libm|libdl|libpthread|librt|libresolv|libutil|ld-linux[^/]*)\.so' \
      | xargs -I{} cp -L {} "$P/lib/"; \
    unset LD_LIBRARY_PATH; \
    patchelf --set-rpath '$ORIGIN' "$P"/lib/*; \
    for m in "$P"/modules/*.so; do patchelf --set-rpath '$ORIGIN/../lib' "$m"; done; \
    patchelf --set-rpath '$ORIGIN/../lib' "$P/sbin/nginx"; \
    install -D /build/nginx/conf/mime.types "$P/conf/mime.types"; \
    install -D /build/nginx/conf/fastcgi_params "$P/conf/fastcgi_params"; \
    install -D /build/modsecurity/modsecurity.conf-recommended "$P/modsec/modsecurity.conf-recommended"; \
    install -D /build/modsecurity/unicode.mapping "$P/modsec/unicode.mapping"; \
    mkdir -p "$P/modsec/crs"; cp -r /build/crs/rules "$P/modsec/crs/rules"; \
    rm -rf "$P/html" "$P/logs"

# A build that does not do what the panel expects of it is not kept. The version
# it actually compiled is written beside the binary: a series build picks the
# newest patch at build time, so the answer is only known here.
RUN set -eux; P="@PREFIX@"; \
    "$P/sbin/nginx" -V 2>&1 | grep -q "built with OpenSSL @OPENSSL_SERIES@"; \
    "$P/sbin/nginx" -v 2>&1 | sed -e 's|^nginx version: nginx/||' > "$P/version"; \
    grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$' "$P/version"; \
    test -f "$P/modules/ngx_http_modsecurity_module.so"; \
    test -f "$P/modules/ngx_cp_attachment_module.so"; \
    test -f "$P/modsec/crs/rules/REQUEST-901-INITIALIZATION.conf"; \
    test -f "$P/conf/mime.types"; test -f "$P/conf/fastcgi_params"; \
    ! ldd "$P/sbin/nginx" "$P"/modules/*.so 2>&1 | grep -q "not found"
LABEL panel.nginx.build="@BUILD_ID@" panel.nginx.crs="@CRS_VERSION@" \
      panel.nginx.openappsec="@OPENAPPSEC_ATTACHMENT_SHA@"
