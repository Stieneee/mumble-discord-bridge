# syntax=docker/dockerfile:experimental

# Stage 1

FROM golang:1.23 as builder
WORKDIR /go/src/app 
COPY . .
RUN apt update && apt install -y libopus-dev
RUN go install github.com/goreleaser/goreleaser@latest
RUN go install github.com/google/go-licenses@latest
RUN goreleaser build --skip-validate
RUN go-licenses save ./cmd/mumble-discord-bridge --force --save_path="./dist/LICENSES"

# Stage 2

FROM alpine:latest as final
WORKDIR /opt/
RUN apk add opus
RUN mkdir /lib64 && ln -s /lib/libc.musl-x86_64.so.1 /lib64/ld-linux-x86-64.so.2
COPY --from=builder /go/src/app/dist/LICENSES .
COPY --from=builder /go/src/app/dist/mumble-discord-bridge_linux_amd64_v1/mumble-discord-bridge .

# Entry Point
CMD ["/opt/mumble-discord-bridge"]
