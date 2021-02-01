# syntax=docker/dockerfile:experimental

# Stage 1

FROM golang:1.15 as builder
WORKDIR /go/src/app 
COPY . .
RUN curl -sfL https://install.goreleaser.com/github.com/goreleaser/goreleaser.sh | sh
RUN ./bin/goreleaser build --skip-validate

# Stage 2

FROM alpine:latest as static
WORKDIR /opt/
RUN apk add opus
COPY --from=builder /go/src/app/dist/mumble-discord-bridge_linux_amd64/mumble-discord-bridge .

# Entry Point
CMD ["/opt/mumble-discord-bridge"]
