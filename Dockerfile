# syntax=docker/dockerfile:experimental

# Stage 1

FROM golang:latest as builder
WORKDIR /go/src/app 
COPY . .

RUN go build -o mumble-discord-bridge -ldflags="-extldflags=-static" *.go 

# Stage 2

FROM alpine:latest as static
WORKDIR /opt/
RUN apk add opus
COPY --from=builder /go/src/app/mumble-discord-bridge .

# Entry Point
CMD ["/opt/mumble-discord-bridge"]
