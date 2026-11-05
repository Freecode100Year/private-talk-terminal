# VIVANT — container image.
#
#   docker build -t vivant .
#   docker run -d --name vivant --network host -v vivant-data:/data vivant
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
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/vivant . \
 && mkdir -p /out/data

# distroless/static: no shell, no package manager; has CA certificates (for
# the Tor exit list) and runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/vivant /usr/local/bin/vivant
COPY --from=build --chown=65532:65532 /out/data /data
ENV VIVANT_ADDR=:2222 \
    VIVANT_HOST_KEY=/data/host_ed25519 \
    VIVANT_HEALTH_ADDR=127.0.0.1:2223
WORKDIR /data
VOLUME /data
EXPOSE 2222 8080
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/vivant"]
