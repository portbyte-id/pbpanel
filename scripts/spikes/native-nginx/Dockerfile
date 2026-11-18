# Spike: build the post-quantum + ModSecurity nginx flavor on a builder image that
# matches the host distro, and emit a relocatable prefix. Mirrors pqcDockerfile in
# internal/nginx/pqc.go, minus the runtime stage and the Core Rule Set.
#
#   docker build --build-arg BASE=ubuntu:26.04 --target out --output type=local,dest=./out .
#
# The output tree is  www/panel/nginx/versions/<BUILD_ID>/{sbin,modules,lib}.
ARG BASE=ubuntu:26.04
FROM ${BASE} AS build
ARG NGINX_SERIES=1.30
ARG OPENSSL_SERIES=3.5
ARG MODSEC_VERSION=3.0.14
ARG MODSEC_NGINX_VERSION=1.0.4
ARG ROOT=/www/panel/nginx
ARG BUILD_ID=spike
ARG WWW_USER=www
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential libpcre2-dev zlib1g-dev perl curl ca-certificates \
      libxml2-dev libyajl-dev liblmdb-dev pkg-config patchelf \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /build

RUN set -eux; \
    for f in "modsecurity|modsecurity-v${MODSEC_VERSION}.tar.gz|https://github.com/owasp-modsecurity/ModSecurity/releases/download/v${MODSEC_VERSION}" \
             "modsecurity-nginx|modsecurity-nginx-v${MODSEC_NGINX_VERSION}.tar.gz|https://github.com/owasp-modsecurity/ModSecurity-nginx/releases/download/v${MODSEC_NGINX_VERSION}"; do \
      dir=${f%%|*}; rest=${f#*|}; name=${rest%%|*}; base=${rest#*|}; \
      curl -fsSLO "$base/$name"; \
      want=$(curl -fsSL "$base/$name.sha256" | cut -d' ' -f1); \
      echo "$want  $name" | sha256sum -c -; \
      mkdir "$dir"; tar -xzf "$name" -C "$dir" --strip-components=1; \
    done

# libmodsecurity goes to a private staging prefix, never /usr: the bundle step
# below copies it and its libraries into the build's own lib directory.
RUN cd modsecurity \
 && ./configure --prefix=/opt/msc --with-pcre2 --with-lmdb --without-curl --without-lua --disable-examples --disable-doxygen-doc \
 && make -j"$(nproc)" \
 && make install

RUN set -eux; \
    nginx_tgz=$(curl -fsSL https://nginx.org/download/ | grep -oE "nginx-${NGINX_SERIES}\.[0-9]+\.tar\.gz" | sort -uV | tail -n1); \
    test -n "$nginx_tgz"; \
    curl -fsSL "https://nginx.org/download/$nginx_tgz" | tar -xz; \
    mv "${nginx_tgz%.tar.gz}" nginx; \
    ossl_tgz=$(curl -fsSL https://www.openssl.org/source/ | grep -oE "openssl-${OPENSSL_SERIES}\.[0-9]+\.tar\.gz" | sort -uV | tail -n1); \
    test -n "$ossl_tgz"; \
    curl -fsSLO "https://www.openssl.org/source/$ossl_tgz"; \
    want=$(curl -fsSL "https://www.openssl.org/source/$ossl_tgz.sha256" | cut -d' ' -f1); \
    echo "$want  $ossl_tgz" | sha256sum -c -; \
    tar -xzf "$ossl_tgz"; \
    mv "${ossl_tgz%.tar.gz}" openssl

WORKDIR /build/nginx
ENV MODSECURITY_INC=/opt/msc/include MODSECURITY_LIB=/opt/msc/lib
RUN set -eux; P="${ROOT}/versions/${BUILD_ID}"; \
    ./configure \
      --prefix="$P" --sbin-path="$P/sbin/nginx" --modules-path="$P/modules" \
      --conf-path="${ROOT}/nginx.conf" \
      --error-log-path=/www/panel/logs/nginx-error.log --http-log-path=/www/panel/logs/nginx-access.log \
      --pid-path=/www/panel/run/nginx.pid --lock-path=/www/panel/run/nginx.lock \
      --http-client-body-temp-path=/www/panel/cache/nginx/client_temp \
      --http-proxy-temp-path=/www/panel/cache/nginx/proxy_temp \
      --http-fastcgi-temp-path=/www/panel/cache/nginx/fastcgi_temp \
      --http-uwsgi-temp-path=/www/panel/cache/nginx/uwsgi_temp \
      --http-scgi-temp-path=/www/panel/cache/nginx/scgi_temp \
      --user="${WWW_USER}" --group="${WWW_USER}" \
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
RUN set -eux; P="${ROOT}/versions/${BUILD_ID}"; mkdir -p "$P/lib"; \
    export LD_LIBRARY_PATH=/opt/msc/lib; \
    for b in "$P/sbin/nginx" "$P"/modules/*.so; do ldd "$b"; done \
      | awk '/=> \//{print $3}' | sort -u \
      | grep -vE '/(libc|libm|libdl|libpthread|librt|libresolv|libutil|ld-linux[^/]*)\.so' \
      | xargs -I{} cp -L {} "$P/lib/"; \
    patchelf --set-rpath '$ORIGIN' "$P"/lib/*; \
    for m in "$P"/modules/*.so; do patchelf --set-rpath '$ORIGIN/../lib' "$m"; done; \
    patchelf --set-rpath '$ORIGIN/../lib' "$P/sbin/nginx"; \
    install -D /build/modsecurity/modsecurity.conf-recommended "$P/modsec/modsecurity.conf-recommended"; \
    install -D /build/modsecurity/unicode.mapping "$P/modsec/unicode.mapping"; \
    rm -rf "$P/html" "$P/conf" "$P/logs"

FROM scratch AS out
ARG ROOT=/www/panel/nginx
COPY --from=build ${ROOT}/versions ${ROOT}/versions
