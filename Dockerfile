# Private-Talk Terminal — container image.
#
#   docker build -t ptterm .
#   docker run -d --name ptterm --network host -v ptterm-data:/data ptterm
#
# Use host networking (or make sure Docker keeps client source addresses):
# behind Docker's userland proxy every user would appear to come from one
# address and share a single rate-limit bucket. See docs/deploy.md.

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS=linux TARGETARCH TARGETVARIANT VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN GOWORK=off go mod download && GOWORK=off go mod verify
COPY . .
RUN GOWORK=off CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/ptterm . \
 && mkdir -p /out/data

# distroless/static: no shell, no package manager; has CA certificates (for
# the Tor exit list) and runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ptterm /usr/local/bin/ptterm
COPY --from=build --chown=65532:65532 /out/data /data
ENV PTT_ADDR=:2222 \
    PTT_HOST_KEY=/data/host_ed25519 \
    PTT_HEALTH_ADDR=127.0.0.1:2223
WORKDIR /data
VOLUME /data
EXPOSE 2222
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/ptterm"]
