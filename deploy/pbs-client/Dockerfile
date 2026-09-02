# proxmox-backup-client, packaged so a Yggdrasil panel needs no host dependency.
#
# Why an image at all: PBS backup targets shell out to Proxmox's backup client,
# and the alternative is telling every operator to add the Proxmox apt repository
# to their panel host. That is a support burden carried forever, on every OS the
# panel runs on. Proxmox ships a statically linked build, so the whole client is
# one 21 MB binary and the image costs almost nothing.
#
# Why busybox rather than scratch: 4 MB buys `docker run --entrypoint /bin/sh`,
# which is how anyone diagnoses this container when a backup misbehaves. A
# scratch image saves nothing that matters and can only be debugged by guessing.
#
# amd64 only. Proxmox does not publish an arm64 build of the client, so there is
# nothing to put in an arm64 image; the panel refuses PBS targets on other
# architectures rather than shipping one that cannot exec (internal/api/pbs.go).

FROM debian:trixie-slim AS fetch

# Pinned to an exact client version: a backup tool is not something to float on
# :latest. Bumping it is a reviewed commit, and CI checks the checksum still
# matches, which is also how a silently-republished artifact would be caught.
ARG PBS_VERSION=4.0.19-1
ARG PBS_SHA256=a5ffc728b115f7bfc97c3094a2b51e2c43b8192f5018dd47701693678cb9d3a5

# Fetched over plain HTTP on purpose, and this is not an oversight worth
# "fixing": download.proxmox.com serves a certificate that does not match its own
# hostname, so HTTPS fails outright. Integrity comes from the pinned sha256
# below, which is a stronger guarantee than transport encryption anyway — the
# same reasoning Debian applies to apt.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends curl ca-certificates; \
    curl -fsSL -o /tmp/client.deb \
      "http://download.proxmox.com/debian/pbs-client/dists/trixie/main/binary-amd64/proxmox-backup-client-static_${PBS_VERSION}_amd64.deb"; \
    echo "${PBS_SHA256}  /tmp/client.deb" | sha256sum -c -; \
    dpkg-deb -x /tmp/client.deb /out; \
    # Fail the build now rather than at somebody's first backup: a dynamically
    # linked binary would exec fine here and not in busybox.
    file /out/usr/bin/proxmox-backup-client 2>/dev/null || true; \
    /out/usr/bin/proxmox-backup-client version

FROM busybox:stable-musl

COPY --from=fetch /out/usr/bin/proxmox-backup-client /usr/bin/proxmox-backup-client
# For a PBS with a certificate from a real CA. Self-signed installs — most
# homelabs — are verified by fingerprint instead (PBS_FINGERPRINT).
COPY --from=fetch /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt

# The client writes tickets and known fingerprints under $HOME. Without one it
# fails in a way that reads like bad credentials; the panel sets HOME too, but an
# operator running this image by hand should not have to know that.
ENV HOME=/tmp

ENTRYPOINT ["/usr/bin/proxmox-backup-client"]
