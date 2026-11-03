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

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/leha-creator/chanly" \
      org.opencontainers.image.description="Chanly backend: api, worker, migrate"
COPY --from=build /out/maxim /maxim
# Корневые сертификаты внешних систем (MAX: сертификат Минцифры, 05 §2.1);
# путь задаётся MAX_CA_CERT_FILE. Как получить и проверить — deploy/certs/README.md.
COPY deploy/certs/ /etc/maxim/certs/
# 65532 — пользователь nonroot в distroless.
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/maxim"]
CMD ["api"]
