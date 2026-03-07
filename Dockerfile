FROM ubuntu:24.04 AS builder

# Go version — keep in sync with go.mod
ARG GO_VERSION=1.25.7

RUN apt-get update && apt-get install -y \
    cmake g++ pkg-config git unzip curl \
    libopus-dev ca-certificates

# Install Go (Ubuntu 24.04 ships older Go)
RUN curl -fsSL https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz | tar -C /usr/local -xzf -
ENV PATH="/usr/local/go/bin:/root/go/bin:$PATH"

# Install libdave C++ library (required for DAVE E2EE)
RUN git clone https://github.com/disgoorg/godave /tmp/godave && \
    cd /tmp/godave && \
    chmod +x scripts/libdave_install.sh && \
    SHELL=/bin/bash ./scripts/libdave_install.sh v1.1.1

ENV CGO_ENABLED=1
ENV PKG_CONFIG_PATH=/root/.local/lib/pkgconfig

WORKDIR /go/src/app
COPY . .
RUN go mod tidy

# Build with version info
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN go build -tags=netgo \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /mumble-discord-bridge \
    ./cmd/mumble-discord-bridge

# Generate licenses
RUN go install github.com/google/go-licenses@latest && \
    go-licenses save ./cmd/mumble-discord-bridge --force --save_path="/LICENSES"

FROM ubuntu:24.04

WORKDIR /opt/
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates libopus0 libstdc++6 && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /LICENSES ./LICENSES
COPY --from=builder /mumble-discord-bridge .
COPY --from=builder /root/.local/lib/libdave.so /usr/lib/
RUN ldconfig

CMD ["/opt/mumble-discord-bridge"]
