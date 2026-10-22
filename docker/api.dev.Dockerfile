# Imagen de desarrollo de la API: Go + air con recarga en caliente.
# El código se monta en /src; los módulos y la caché de build van en volúmenes.
FROM golang:1.27-alpine
RUN apk add --no-cache git ca-certificates \
 && go install github.com/air-verse/air@latest \
 && go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
WORKDIR /src
ENV CLOUDMESH_ADDR=:8080 CLOUDMESH_DATA_DIR=/data CLOUDMESH_LOG_LEVEL=debug
COPY docker/dev-entrypoint.sh /usr/local/bin/dev-entrypoint
RUN chmod +x /usr/local/bin/dev-entrypoint
EXPOSE 8080
ENTRYPOINT ["dev-entrypoint"]
CMD ["air", "-c", ".air.docker.toml"]
