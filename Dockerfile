# Build stage - Alpine for musl consistency
FROM golang:1.25-alpine AS builder

WORKDIR /go/src/app
COPY . .

# Install build dependencies
RUN apk add --no-cache git make opus-dev gcc musl-dev

# Build with version info
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=1 go build -tags=netgo \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /mumble-discord-bridge \
    ./cmd/mumble-discord-bridge

# Generate licenses
RUN go install github.com/google/go-licenses@latest && \
    go-licenses save ./cmd/mumble-discord-bridge --force --save_path="/LICENSES"

# Runtime stage - Alpine
FROM alpine:latest

WORKDIR /opt/
RUN apk add --no-cache opus

COPY --from=builder /LICENSES ./LICENSES
COPY --from=builder /mumble-discord-bridge .

CMD ["/opt/mumble-discord-bridge"]
