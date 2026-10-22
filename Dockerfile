# syntax=docker/dockerfile:1

# ---- 1. Frontend -----------------------------------------------------------
FROM node:24-alpine AS web
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# ---- 2. Backend ------------------------------------------------------------
FROM golang:1.27-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/cloudmesh ./cmd/cloudmesh
RUN mkdir -p /out/data

# ---- 3. Imagen final -------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/cloudmesh /cloudmesh
# /data lo monta el volumen. Se crea en la imagen con dueño nonroot (uid 65532)
# para que los volúmenes nombrados hereden el permiso de escritura.
COPY --from=build --chown=nonroot:nonroot /out/data /data
VOLUME /data
# GOGC más agresivo: las transferencias mueven buffers de decenas de MiB y el
# recolector por defecto deja crecer el heap al doble antes de actuar.
ENV CLOUDMESH_ADDR=:8080 CLOUDMESH_DATA_DIR=/data GOGC=50
EXPOSE 8080 9000
USER nonroot:nonroot
HEALTHCHECK --interval=10s --timeout=3s --retries=5 CMD ["/cloudmesh", "healthcheck"]
ENTRYPOINT ["/cloudmesh"]
CMD ["serve"]
