# Use a two-stage build to reduce the complexity of building for alpine

FROM golang:1.25 AS builder
WORKDIR /go/src/app
COPY . .
RUN apt update && apt install -y libopus-dev
RUN go install github.com/goreleaser/goreleaser/v2@latest
RUN go install github.com/google/go-licenses@latest
RUN goreleaser build --skip=validate --single-target
RUN go-licenses save ./cmd/mumble-discord-bridge --force --save_path="./dist/LICENSES"

FROM alpine:latest AS final
WORKDIR /opt/
RUN apk add opus

ARG TARGETARCH
RUN if [ "$TARGETARCH" = "amd64" ]; then \
      mkdir /lib64 && ln -s /lib/libc.musl-x86_64.so.1 /lib64/ld-linux-x86-64.so.2; \
    elif [ "$TARGETARCH" = "arm64" ]; then \
      mkdir -p /lib && ln -s /lib/ld-musl-aarch64.so.1 /lib/ld-linux-aarch64.so.1 2>/dev/null || true; \
    fi

COPY --from=builder /go/src/app/dist/LICENSES .
COPY --from=builder /go/src/app/dist/mumble-discord-bridge_linux_*/mumble-discord-bridge .

# Entry Point
CMD ["/opt/mumble-discord-bridge"]
