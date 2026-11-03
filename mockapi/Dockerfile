# syntax=docker/dockerfile:1
# Образ заглушки API. Собирается из корня репозитория:
#   docker build -f mockapi/Dockerfile -t maxim-mockapi .

FROM golang:1.27.2-alpine AS build
WORKDIR /src

COPY mockapi/go.mod mockapi/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY mockapi/ ./
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/mockapi ./cmd/mockapi

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/leha-creator/chanly" \
      org.opencontainers.image.description="Chanly mock API (in-memory demo data)"
COPY --from=build /out/mockapi /mockapi
# 65532 — пользователь nonroot в distroless.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/mockapi"]
