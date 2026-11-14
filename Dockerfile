# syntax=docker/dockerfile:1

# Build the KTRX CLI as a static Linux binary.
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

# Minimal runtime image. Kubernetes access is supplied by the user at runtime.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates && addgroup -S ktrx && adduser -S -G ktrx ktrx
COPY --from=build /out/ktrx /usr/local/bin/ktrx
USER ktrx
ENTRYPOINT ["/usr/local/bin/ktrx"]
CMD ["--help"]
