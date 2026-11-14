# syntax=docker/dockerfile:1

# Build the KTRX CLI as a static Linux binary on the native build platform.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -trimpath -ldflags="-s -w" -o /out/ktrx ./cmd/ktrx

# No RUN commands in the target-platform stage: this supports multi-arch
# builds even when QEMU/binfmt emulation is not installed on the builder.
FROM alpine:3.21
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/ktrx /usr/local/bin/ktrx
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/ktrx"]
CMD ["--help"]
