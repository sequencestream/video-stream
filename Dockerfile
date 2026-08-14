# vs, with the two things it shells out to: ffmpeg and faster-whisper.
#
# The image exists so the recognition stack can be had without installing
# Python packages on the host. It is not small — the speech model runtime and a
# full ffmpeg are most of it — and that is the trade being made.
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
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/vs ./cmd/vs

FROM alpine:3.22

# ffmpeg must be the full build: the `subtitles` filter needs libass, and
# without it `vs subtitle -mode burn` cannot work at all. font-noto-cjk is what
# makes burned-in Chinese render as glyphs rather than as boxes.
RUN apk add --no-cache ca-certificates ffmpeg font-noto-cjk py3-pip \
 && pip3 install --no-cache-dir --break-system-packages faster-whisper \
 && adduser -D -u 10001 app

COPY --from=build /out/vs /usr/local/bin/vs

# Models are downloaded on first use and cached here. Mount a volume over it,
# or the download repeats on every container.
ENV VS_ASR_MODEL_DIR=/models
RUN mkdir -p /models /work && chown -R app:app /models /work

USER app
WORKDIR /work

ENTRYPOINT ["vs"]
CMD ["--help"]
