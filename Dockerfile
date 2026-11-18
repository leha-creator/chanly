# syntax=docker/dockerfile:1
# Образ бэкенда: один исполняемый файл с режимами api, worker, migrate, legal.
# Собирается из корня репозитория:
#   docker build -t maxim .
# Контекст ограничивает Dockerfile.dockerignore (корневой .dockerignore — для mockapi).

FROM golang:1.27.2-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
COPY templates/ templates/
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/maxim ./cmd/maxim

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 65532 maxim && useradd --uid 65532 --gid 65532 --no-create-home maxim
RUN mkdir -p /home/nonroot && chown 65532:65532 /home/nonroot && usermod --home /home/nonroot maxim
ENV HOME=/home/nonroot
LABEL org.opencontainers.image.source="https://github.com/leha-creator/chanly" \
      org.opencontainers.image.description="Chanly backend: api, worker, migrate"
COPY --from=build /out/maxim /maxim
# Корневые сертификаты внешних систем (MAX: сертификат Минцифры, 05 §2.1);
# путь задаётся MAX_CA_CERT_FILE. Как получить и проверить — deploy/certs/README.md.
COPY deploy/certs/ /etc/maxim/certs/
# 65532 — непривилегированный пользователь; FFmpeg запускается только worker через prlimit.
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/maxim"]
CMD ["api"]
