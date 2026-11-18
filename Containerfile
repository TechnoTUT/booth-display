# syntax=docker/dockerfile:1
# booth-display: the Go controller (display-controller, Web GUI embedded) and
# the Rust media core (booth-core) in one image. Build from a clone with the
# moip-engine submodule checked out (git submodule update --init):
#
#   podman build -t booth-display -f Containerfile .
#
# The NDI runtime is not in the image (NDI SDK license): the Kubernetes sample
# in deploy/k8s fetches it in an init container with install-ndi-sdk.sh.

# ---- Web GUI (Vue 3) ----
FROM docker.io/library/node:22-trixie-slim AS web
WORKDIR /src/controller/frontend
COPY controller/frontend/package.json controller/frontend/package-lock.json ./
RUN npm ci
COPY controller/frontend/ ./
# vite writes to ../web/dist, which the Go controller embeds
RUN npm run build

# ---- Media core (Rust) ----
# libva, libx264 and libndi are loaded at run time: no headers or libraries needed.
FROM docker.io/library/rust:1-slim-trixie AS core
WORKDIR /src
COPY moip-engine/ moip-engine/
COPY core/ core/
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/core/target \
    test -f moip-engine/ndi/Cargo.toml \
 && cargo build --release --locked --manifest-path core/Cargo.toml \
 && install -m 755 core/target/release/booth-core /booth-core

# ---- Controller (Go) ----
FROM docker.io/library/golang:1.26-trixie AS controller
WORKDIR /src/controller
COPY controller/go.mod controller/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY controller/ ./
COPY --from=web /src/controller/web/dist/ web/dist/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /display-controller ./cmd/server

# ---- Runtime ----
FROM docker.io/library/debian:trixie-slim
# - libx264-164: software encoder (fallback when VA-API is unavailable)
# - libva2, libva-drm2 and the Intel drivers: VA-API encoder. iHD (non-free,
#   Broadwell and newer) and i965 (Ivy Bridge / Haswell); LIBVA_DRIVER_NAME picks one
# - ffmpeg: video-file and rainbow input, and the vaapi-ffmpeg encoder
# - curl, ca-certificates: install-ndi-sdk.sh in the init container
RUN sed -i 's/^Components: main$/Components: main non-free/' /etc/apt/sources.list.d/debian.sources \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      libx264-164 libva2 libva-drm2 intel-media-va-driver-non-free i965-va-driver \
      ffmpeg curl ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --user-group --home-dir /data booth \
 && mkdir -p /data /ndi \
 && chown booth:booth /data /ndi

# Keep the two binaries together: display-controller looks for booth-core next to itself.
COPY --from=controller /display-controller /usr/local/bin/display-controller
COPY --from=core /booth-core /usr/local/bin/booth-core
COPY scripts/install-ndi-sdk.sh /usr/local/bin/install-ndi-sdk.sh
COPY container/entrypoint.sh /usr/local/bin/booth-display-entrypoint
# Seed for /data/config.yaml (copied once; the Web GUI saves its changes there)
COPY config.yaml /etc/booth-display/config.yaml

USER booth
WORKDIR /data
ENV HOME=/data \
    NDI_RUNTIME_DIR_V6=/ndi
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["booth-display-entrypoint"]
