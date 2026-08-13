# Go service image: vsd (daemon) and vs (CLI) in one image. There is no UI
# stage and no JavaScript anywhere in the build — the product surface is the
# CLI, which agents drive.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Overridable so builds behind a restricted network can point at a reachable
# module mirror, e.g. --build-arg GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=0.1.0-dev
# CGO_ENABLED=0 with the pure-Go SQLite driver yields a static binary that runs
# on a bare scratch-like base.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/vsd ./cmd/vsd \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/vs  ./cmd/vs

FROM alpine:3.22

# wget backs the compose healthcheck, ffmpeg renders the final media, and
# ca-certificates is needed once providers are called over TLS.
RUN apk add --no-cache ca-certificates ffmpeg font-noto-cjk py3-pip wget \
 && pip3 install --no-cache-dir --break-system-packages edge-tts==7.2.8 \
 && adduser -D -u 10001 app

COPY --from=build /out/vsd /usr/local/bin/vsd
COPY --from=build /out/vs  /usr/local/bin/vs

RUN mkdir -p /var/lib/video-stream /var/lib/video-stream/output /var/lib/video-stream/media \
 && chown -R app:app /var/lib/video-stream

USER app
WORKDIR /var/lib/video-stream

ENV VS_SERVER_ADDR=:8080 \
    VS_DATA_DIR=/var/lib/video-stream \
    VS_OUTPUT_DIR=/var/lib/video-stream/output \
    VS_MEDIA_DIR=/var/lib/video-stream/media

EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["vsd"]
