#!/bin/sh
# Carga claves de desarrollo persistidas en el volumen de datos. Si no se
# pasaron por entorno, las genera una vez y las reutiliza en cada arranque,
# así la base cifrada sigue siendo legible tras reiniciar el contenedor.
set -e
KEYS="${CLOUDMESH_DATA_DIR:-/data}/.dev-keys"
if [ -z "$CLOUDMESH_MASTER_KEY" ] || [ -z "$CLOUDMESH_SESSION_SECRET" ]; then
  if [ ! -f "$KEYS" ]; then
    mkdir -p "$(dirname "$KEYS")"
    {
      echo "CLOUDMESH_MASTER_KEY=$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
      echo "CLOUDMESH_SESSION_SECRET=$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
    } > "$KEYS"
    chmod 600 "$KEYS"
    echo "dev-entrypoint: claves de desarrollo generadas en $KEYS" >&2
  fi
  set -a
  . "$KEYS"
  set +a
fi
exec "$@"
