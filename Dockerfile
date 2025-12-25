FROM mcr.microsoft.com/oss/go/microsoft/golang:1.25-azurelinux3.0@sha256:1523c0cf32705470e2be1667fefb8ca3f041a309745b6e11a32c917c643216db AS builder
ARG TARGETOS
ARG TARGETARCH

RUN if [ "${TARGETARCH}" = "arm64" ]; then \
    tdnf install -y build-essential && tdnf clean all; \
    fi

WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# Cache deps before building and copying source so that we don't need to
# re-download as much and so that source changes don't invalidate our downloaded
# layer.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Copy the go source.
COPY cmd/ cmd/
COPY internal/ internal/

ARG VERSION
ARG GIT_COMMIT
ARG BUILD_DATE
ARG BUILD_ID

# Build the GOARCH has not a default value to allow the binary be built
# according to the host where the command was called. For example, if we call
# make docker-build in a local env which has the Apple Silicon M1 SO the docker
# BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be
# linux/amd64. Therefore, by leaving it empty we can ensure that the container
# and binary shipped on it will have the same platform.
#
ARG LDFLAGS="\
    -X local-csi-driver/internal/pkg/version.version=${VERSION} \
    -X local-csi-driver/internal/pkg/version.gitCommit=${GIT_COMMIT} \
    -X local-csi-driver/internal/pkg/version.buildDate=${BUILD_DATE} \
    -X local-csi-driver/internal/pkg/version.buildId=${BUILD_ID}"

# CGO_ENABLED=1 is required to build the driver with FIPS support.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -v -ldflags "${LDFLAGS}" -o local-csi-driver cmd/driver/main.go


FROM mcr.microsoft.com/azurelinux/base/core:3.0@sha256:72b38469d220910172d0f52dcb029b3cbbb226c63522103c955778ad7ace06a8 AS dependency-install
RUN tdnf install -y --releasever 3.0 --installroot /staging \
    e2fsprogs \
    lvm2 \
    # ensure that libcrypto.so.X is available for dlopen for fips builds
    openssl-libs \
    util-linux \
    xfsprogs \
    && tdnf clean all \
    && rm -rf /staging/run /staging/var/log /staging/var/cache/tdnf

# Use distroless as minimal base image to package the driver binary.
FROM mcr.microsoft.com/azurelinux/distroless/minimal:3.0@sha256:c8917363fff9f80859b0e4140cf1d3677fb29cc8f48c0337e53dd58797e9f3ee
WORKDIR /
COPY --from=builder /workspace/local-csi-driver .
COPY --from=dependency-install /staging /

# Set the environment variable to disable udev and just use lvm.
ENV DM_DISABLE_UDEV=1

ENTRYPOINT ["/local-csi-driver"]
