package nginx

import (
	"fmt"
	"strings"
)

// PQCPrefix marks a version of the post-quantum flavor. The flavor is part of the
// version string ("pqc-1.30"), so each flavor and series is its own image family
// with its own builds, rollback and active choice, and nothing else in the
// version handling needs to know there are two.
const PQCPrefix = "pqc-"

// pqcBase is the image the post-quantum flavor is built on. The official nginx
// image is Debian and links the system OpenSSL, which has no ML-KEM; this flavor
// compiles nginx against OpenSSL 3.5 on Alpine instead.
const pqcBase = "alpine:3.22"

// openSSLSeries is the OpenSSL series the PQC flavor links statically. 3.5 is the
// first with ML-KEM and ML-DSA in the default provider, and it is a long-term
// release. With it nginx offers the hybrid X25519MLKEM768 group out of the box,
// so no ssl_ecdh_curve line is needed.
const openSSLSeries = "3.5"

// Versions of libmodsecurity (ModSecurity v3) and of its nginx connector, built
// into the PQC flavor as a dynamic module. The module is only loaded by a
// `load_module modules/ngx_http_modsecurity_module.so;` line, so the flavor
// behaves like plain nginx until the configuration asks for it.
const (
	modSecurityVersion      = "3.0.14"
	modSecurityNginxVersion = "1.0.4"
)

// The OWASP Core Rule Set bundled with the flavor, the rules ModSecurity runs
// when a site's firewall is on. The release tarball is pinned and checked
// against the digest GitHub publishes for the asset, so a build never takes
// rules nobody looked at. The Dockerfile unpacks it at waf.CRSDir.
const (
	// CRSVersion is the Core Rule Set release.
	CRSVersion = "4.30.0"
	crsSHA256  = "3d678a41fd5aade34760127fef5dd64fd7a77848913fc0f70dde0cf467c94427"
)

// IsPQC reports whether v is a version of the post-quantum flavor.
func IsPQC(v string) bool { return strings.HasPrefix(v, PQCPrefix) }

// Series is the nginx MAJOR.MINOR a version builds, without the flavor.
func Series(v string) string { return strings.TrimPrefix(v, PQCPrefix) }

// pqcDockerfile builds nginx from source on Alpine against a source-built
// OpenSSL 3.5, on the host that runs it. The newest patch release of each series
// is looked up at build time, so an update is a rebuild that picks up fixes, the
// same as it is for the official image. The OpenSSL tarball is checked against
// the digest published beside it.
func pqcDockerfile(version string) []byte {
	return fmt.Appendf(nil, `FROM %[1]s AS build
ARG NGINX_SERIES=%[2]s
ARG OPENSSL_SERIES=%[3]s
ARG MODSEC_VERSION=%[7]s
ARG MODSEC_NGINX_VERSION=%[8]s
RUN apk add --no-cache build-base linux-headers pcre2-dev zlib-dev perl curl ca-certificates \
      libxml2-dev yajl-dev curl-dev lmdb-dev
WORKDIR /build
# libmodsecurity (ModSecurity v3) and its nginx connector, from the release
# tarballs, each checked against the digest published beside it.
RUN set -eux; \
    for f in "modsecurity|modsecurity-v${MODSEC_VERSION}.tar.gz|https://github.com/owasp-modsecurity/ModSecurity/releases/download/v${MODSEC_VERSION}" \
             "modsecurity-nginx|modsecurity-nginx-v${MODSEC_NGINX_VERSION}.tar.gz|https://github.com/owasp-modsecurity/ModSecurity-nginx/releases/download/v${MODSEC_NGINX_VERSION}"; do \
      dir=${f%%%%|*}; rest=${f#*|}; name=${rest%%%%|*}; base=${rest#*|}; \
      curl -fsSLO "$base/$name"; \
      want=$(curl -fsSL "$base/$name.sha256" | cut -d' ' -f1); \
      echo "$want  $name" | sha256sum -c -; \
      mkdir "$dir"; tar -xzf "$name" -C "$dir" --strip-components=1; \
    done
RUN cd modsecurity \
 && ./configure --prefix=/usr --with-pcre2 --with-lmdb --without-lua --disable-examples --disable-doxygen-doc \
 && make -j"$(nproc)" \
 && make install
# The OWASP Core Rule Set, pinned and checked against the published digest.
ARG CRS_VERSION=%[9]s
ARG CRS_SHA256=%[10]s
RUN set -eux; \
    curl -fsSLO "https://github.com/coreruleset/coreruleset/releases/download/v${CRS_VERSION}/coreruleset-${CRS_VERSION}-minimal.tar.gz"; \
    echo "${CRS_SHA256}  coreruleset-${CRS_VERSION}-minimal.tar.gz" | sha256sum -c -; \
    mkdir crs; tar -xzf "coreruleset-${CRS_VERSION}-minimal.tar.gz" -C crs --strip-components=1; \
    test -d crs/rules
RUN set -eux; \
    nginx_tgz=$(curl -fsSL https://nginx.org/download/ | grep -oE "nginx-${NGINX_SERIES}\.[0-9]+\.tar\.gz" | sort -uV | tail -n1); \
    test -n "$nginx_tgz"; \
    curl -fsSL "https://nginx.org/download/$nginx_tgz" | tar -xz; \
    mv "${nginx_tgz%%.tar.gz}" nginx; \
    ossl_tgz=$(curl -fsSL https://www.openssl.org/source/ | grep -oE "openssl-${OPENSSL_SERIES}\.[0-9]+\.tar\.gz" | sort -uV | tail -n1); \
    test -n "$ossl_tgz"; \
    curl -fsSLO "https://www.openssl.org/source/$ossl_tgz"; \
    want=$(curl -fsSL "https://www.openssl.org/source/$ossl_tgz.sha256" | cut -d' ' -f1); \
    echo "$want  $ossl_tgz" | sha256sum -c -; \
    tar -xzf "$ossl_tgz"; \
    mv "${ossl_tgz%%.tar.gz}" openssl
WORKDIR /build/nginx
ENV MODSECURITY_INC=/usr/include MODSECURITY_LIB=/usr/lib
RUN ./configure \
      --modules-path=/usr/lib/nginx/modules \
      --add-dynamic-module=/build/modsecurity-nginx \
      --prefix=/etc/nginx --sbin-path=/usr/sbin/nginx --conf-path=/etc/nginx/nginx.conf \
      --error-log-path=/var/log/nginx/error.log --http-log-path=/var/log/nginx/access.log \
      --pid-path=/run/nginx.pid --lock-path=/run/nginx.lock \
      --http-client-body-temp-path=/var/cache/nginx/client_temp \
      --http-proxy-temp-path=/var/cache/nginx/proxy_temp \
      --http-fastcgi-temp-path=/var/cache/nginx/fastcgi_temp \
      --http-uwsgi-temp-path=/var/cache/nginx/uwsgi_temp \
      --http-scgi-temp-path=/var/cache/nginx/scgi_temp \
      --user=%[4]s --group=%[4]s \
      --with-openssl=/build/openssl --with-openssl-opt="no-tests" \
      --with-cc-opt="-O2" \
      --with-compat --with-threads --with-file-aio \
      --with-http_ssl_module --with-http_v2_module --with-http_v3_module \
      --with-http_realip_module --with-http_gzip_static_module --with-http_gunzip_module \
      --with-http_slice_module --with-http_stub_status_module --with-http_auth_request_module \
      --with-http_secure_link_module --with-http_sub_module --with-http_addition_module \
      --with-stream --with-stream_ssl_module --with-stream_ssl_preread_module --with-stream_realip_module \
 && make -j"$(nproc)" \
 && make install

FROM %[1]s
RUN apk add --no-cache pcre2 zlib libstdc++ libxml2 yajl libcurl lmdb \
 && addgroup -g %[5]d %[4]s \
 && adduser -D -H -u %[6]d -G %[4]s -h /nonexistent -s /sbin/nologin %[4]s \
 && mkdir -p /var/cache/nginx /var/log/nginx /run \
 && ln -sf /dev/stdout /var/log/nginx/access.log \
 && ln -sf /dev/stderr /var/log/nginx/error.log
COPY --from=build /usr/sbin/nginx /usr/sbin/nginx
COPY --from=build /etc/nginx /etc/nginx
COPY --from=build /usr/lib/nginx/modules /usr/lib/nginx/modules
COPY --from=build /usr/lib/libmodsecurity.so* /usr/lib/
COPY --from=build /build/modsecurity/modsecurity.conf-recommended /etc/nginx/modsec/modsecurity.conf-recommended
COPY --from=build /build/modsecurity/unicode.mapping /etc/nginx/modsec/unicode.mapping
COPY --from=build /build/crs/rules /etc/nginx/modsec/crs/rules
RUN nginx -V 2>&1 | grep -q "built with OpenSSL ${OPENSSL_SERIES}" \
 && test -f /usr/lib/nginx/modules/ngx_http_modsecurity_module.so \
 && test -f /etc/nginx/modsec/crs/rules/REQUEST-901-INITIALIZATION.conf \
 && ! ldd /usr/lib/nginx/modules/ngx_http_modsecurity_module.so 2>&1 | grep -qE "Error loading|No such file"
ARG CRS_VERSION=%[9]s
LABEL panel.nginx.flavor="pqc" panel.nginx.crs="${CRS_VERSION}"
EXPOSE 80 443 443/udp
STOPSIGNAL SIGQUIT
`, pqcBase, version, openSSLSeries, WWWUser, WWWGID, WWWUID, modSecurityVersion, modSecurityNginxVersion, CRSVersion, crsSHA256)
}
