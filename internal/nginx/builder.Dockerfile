# The native nginx build. It runs on a builder image that matches the host (same
# distro and release), so the binary links the host's own glibc, and it leaves a
# self-contained prefix: nginx, the ModSecurity module, every shared library they
# need except the glibc core, the Core Rule Set and mime.types.
#
# The panel reads the prefix out of the finished image and the image is thrown
# away. The upper-case words between two at-signs are placeholders that
# builder.go fills in.
FROM @BASE@
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential libpcre2-dev zlib1g-dev perl curl ca-certificates \
      libxml2-dev libyajl-dev liblmdb-dev pkg-config patchelf \
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

# The newest patch release of each series is looked up at build time, so an update
# is a rebuild that picks up fixes. The OpenSSL tarball is checked against the
# digest published beside it.
RUN set -eux; \
    nginx_tgz=$(curl -fsSL https://nginx.org/download/ | grep -oE "nginx-@SERIES@\.[0-9]+\.tar\.gz" | sort -uV | tail -n1); \
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

# Bundle every shared library the binary and the module need, except the glibc
# core, which has to be the host's own. RUNPATH is not inherited by a library's
# dependencies, so every bundled library gets $ORIGIN as its own.
RUN set -eux; P="@PREFIX@"; mkdir -p "$P/lib" "$P/conf"; \
    export LD_LIBRARY_PATH=/opt/msc/lib; \
    for b in "$P/sbin/nginx" "$P"/modules/*.so; do ldd "$b"; done \
      | awk '/=> \//{print $3}' | sort -u \
      | grep -vE '/(libc|libm|libdl|libpthread|librt|libresolv|libutil|ld-linux[^/]*)\.so' \
      | xargs -I{} cp -L {} "$P/lib/"; \
    patchelf --set-rpath '$ORIGIN' "$P"/lib/*; \
    for m in "$P"/modules/*.so; do patchelf --set-rpath '$ORIGIN/../lib' "$m"; done; \
    patchelf --set-rpath '$ORIGIN/../lib' "$P/sbin/nginx"; \
    install -D /build/nginx/conf/mime.types "$P/conf/mime.types"; \
    install -D /build/nginx/conf/fastcgi_params "$P/conf/fastcgi_params"; \
    install -D /build/modsecurity/modsecurity.conf-recommended "$P/modsec/modsecurity.conf-recommended"; \
    install -D /build/modsecurity/unicode.mapping "$P/modsec/unicode.mapping"; \
    mkdir -p "$P/modsec/crs"; cp -r /build/crs/rules "$P/modsec/crs/rules"; \
    rm -rf "$P/html" "$P/logs"

# A build that does not do what the panel expects of it is not kept.
RUN set -eux; P="@PREFIX@"; \
    "$P/sbin/nginx" -V 2>&1 | grep -q "built with OpenSSL @OPENSSL_SERIES@"; \
    test -f "$P/modules/ngx_http_modsecurity_module.so"; \
    test -f "$P/modsec/crs/rules/REQUEST-901-INITIALIZATION.conf"; \
    test -f "$P/conf/mime.types"; test -f "$P/conf/fastcgi_params"; \
    ! ldd "$P/sbin/nginx" "$P"/modules/*.so 2>&1 | grep -q "not found"
LABEL panel.nginx.build="@BUILD_ID@" panel.nginx.crs="@CRS_VERSION@"
