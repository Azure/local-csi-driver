FROM mcr.microsoft.com/oss/go/microsoft/golang:1.26-azurelinux3.0@sha256:dea066b5b9108bf842e4a43a1470bbf7f733e4afded04192c88da798bb015062 AS builder
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
    CGO_ENABLED=1 GOEXPERIMENT=systemcrypto GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -v -ldflags "${LDFLAGS}" -o local-csi-driver cmd/driver/main.go


# Generate NOTICE.txt from dependency licenses. Built in parallel with `builder`.
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.26-azurelinux3.0@sha256:dea066b5b9108bf842e4a43a1470bbf7f733e4afded04192c88da798bb015062 AS notice
ARG GO_LICENSES_VERSION=v2.0.1
WORKDIR /workspace
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOBIN=/usr/local/bin go install github.com/google/go-licenses/v2@${GO_LICENSES_VERSION}
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY NOTICE.tmpl NOTICE.manual ./
COPY hack/generate-notice.sh hack/generate-notice.sh
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    ./hack/generate-notice.sh NOTICE.txt


FROM mcr.microsoft.com/azurelinux/base/core:3.0@sha256:f5e224c47997aa4a5d3d8addfcc3866e175e7026368a71ce1be2c0eed1876f04 AS dependency-install
RUN tdnf install -y --releasever 3.0 --installroot /staging \
    e2fsprogs \
    lvm2 \
    # ensure that libcrypto.so.X is available for dlopen for fips builds
    openssl-libs \
    openssl \
    util-linux \
    xfsprogs \
    && tdnf clean all \
    && rm -rf /staging/run /staging/var/log /staging/var/cache/tdnf

# Use distroless as minimal base image to package the driver binary.
FROM mcr.microsoft.com/azurelinux/distroless/minimal:3.0@sha256:0c64ab9cfc44d4f100c0590bd59ead9afedda6cc54f14bb7465b5f9c35ddc037
WORKDIR /
COPY --from=builder /workspace/local-csi-driver .
COPY --from=dependency-install /staging /
COPY --from=notice /workspace/NOTICE.txt /

# Set the environment variable to disable udev and just use lvm.
ENV DM_DISABLE_UDEV=1

ENTRYPOINT ["/local-csi-driver"]
