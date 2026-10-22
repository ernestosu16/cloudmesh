# CloudMesh — Plan del proyecto

Fecha: 2026-10-21. Estado: fases 1 a 13 completadas el 2026-10-21; repositorio en GitHub con CI. Pendiente: prueba con Mega real, despliegue en Dokploy y muestreo de hashes en el scrub.

## 1. Qué es

Gateway de almacenamiento unificado: varias cuentas de nube se ven y se usan como un solo drive virtual, al estilo de un RAID sobre nubes. Los archivos se trocean en bloques al subir, se reparten entre cuentas y se protegen con paridad Reed–Solomon configurable (`k+m`), de modo que la pérdida de hasta `m` cuentas no pierde datos. Uso interno (un tenant, pocos usuarios, sin facturación). Proveedor v1: **Mega**. El diseño deja la puerta abierta a Google Drive, OneDrive, Dropbox y S3 sin reescribir el núcleo.

Referencia funcional: `../9drive` (Express + Prisma + MySQL, Google Drive y S3). CloudMesh toma las mismas ideas de producto (cuentas conectadas, cuota agregada, carpetas virtuales, enrutado de subidas, streaming sin tocar disco) pero en Go, con un solo binario y un solo contenedor.

## 2. Alcance v1

Incluido:

- Login interno con email y contraseña, sesiones por cookie HttpOnly. Registro público desactivado. En el primer arranque sin usuarios la app muestra una página de configuración inicial para crear el administrador; para que nadie que llegue antes se lo apropie, el servidor genera un código de un solo uso y lo escribe en el log (`docker logs`). El código vive en memoria: cada reinicio sin usuarios genera otro. Usuarios adicionales con `cloudmesh create-user`.
- Conectar N cuentas Mega (email, contraseña y código 2FA si aplica). Se guardan cifrados **session id + master key**, nunca la contraseña. Si Mega invalida la sesión, la cuenta pasa a estado `reconnect_required`.
- Cuota por cuenta y cuota agregada (usado / total).
- Drive virtual: carpetas virtuales propias de CloudMesh, independientes de la estructura en Mega. Cada archivo del índice apunta a `(cuenta, handle Mega)`.
- Subida en streaming navegador → Go → Mega, sin escribir a disco. El archivo se corta en franjas de `k` bloques de datos más `m` de paridad (Reed–Solomon); cada bloque se sube como un archivo opaco a una cuenta distinta elegida por la política de enrutado: `most_free` (defecto), `round_robin`, `priority`. Con menos de `k+m` cuentas disponibles el archivo se guarda como `1+0` (entero en una cuenta) y se avisa.
- Descarga en streaming Mega → Go → navegador leyendo `k` bloques por franja; si falta alguno se reconstruye con la paridad. Soporte de `Range` a granularidad de franja para previews de vídeo y audio.
- Listar, buscar por nombre, renombrar, mover entre carpetas virtuales, eliminar (a papelera de Mega).
- Sincronización: job que recorre el árbol de cada cuenta bajo la carpeta raíz `cloudmesh/` y reconcilia el índice (archivos subidos desde la app oficial de Mega aparecen; borrados desaparecen).
- Previews: imagen, vídeo, audio, PDF, texto.
- Panel de progreso de subidas en el frontend.
- Despliegue con Docker en Dokploy.

Excluido de v1 (fases posteriores): scrub y rebuild automáticos (fase 6), manifiesto de recuperación del índice en cada nube (fase 6), enlaces públicos compartidos (hechos en la fase 8), API keys para subida externa (API S3, fase 9), segundo proveedor, multi-tenant, deduplicación.

## 3. Stack y versiones

Verificado el 2026-10-21:

| Pieza | Versión | Nota |
|---|---|---|
| Go | 1.27 (local: go1.27.1) | Imagen `golang:1.27-alpine` para build |
| Router HTTP | `net/http` stdlib | Patrones de método y path nativos; sin framework |
| DB | SQLite vía `modernc.org/sqlite` | Pure Go, sin CGO, un archivo en el volumen `/data` |
| Queries | `sqlc` | SQL tipado, sin ORM |
| Migraciones | `pressly/goose` con SQL embebido | Se aplican al arrancar |
| Mega | `github.com/t3rm1n4l/go-mega` (commit 2026-07-17) | Mismo cliente que usa rclone; soporta `MultiFactorLogin`, `LoginWithKeys`, `GetQuota`, `NewUpload`/`UploadChunk`, `NewDownload`/`DownloadChunk`, `Link`, `Move`, `Rename`, `Delete`, `CreateDir` |
| Cifrado de secretos | `crypto/aes` GCM, clave en `CLOUDMESH_MASTER_KEY` | stdlib |
| Paridad | `github.com/klauspost/reedsolomon` | Mismo códec que MinIO y Backblaze; SIMD en x86 y ARM |
| Node (build front) | 24 LTS (26 pasa a LTS el 2026-10-28) | Solo en etapa de build de la imagen |
| Gestor paquetes | pnpm 10.33 | Fijado en `packageManager` para que corepack use la misma versión en Docker |
| React | 19.3 | |
| Vite | 8 (Rolldown) | |
| TypeScript | 6.0 | strict; `baseUrl` ya no se usa, `paths` relativo al tsconfig |
| UI | Tailwind CSS 4 + shadcn/ui | Mobile-first obligatorio, verificar a 375px |
| Datos/rutas | TanStack Query 5 + TanStack Router | |
| Formularios | react-hook-form + zod | Login sigue con `useState`; cuentas usa RHF |
| Docker | 29 | Multi-stage, imagen final distroless |
| Despliegue | Dokploy (Compose) | Traefik de Dokploy pone HTTPS y dominio |

Decisión sobre la capa de proveedor: interfaz propia `storage.Provider` pequeña, con un único adaptador `mega` en v1 que envuelve `go-mega`. Se descartó embeber rclone como librería por ahora: trae un árbol de dependencias enorme para un solo proveedor. Si en una fase posterior hay 3 o más proveedores, el adaptador rclone se puede añadir detrás de la misma interfaz sin tocar el resto.

## 4. Arquitectura

Un solo proceso Go sirve la API en `/api/*` y el frontend compilado embebido con `embed.FS` en `/`. Mismo origen: cookies de sesión sin CORS ni JWT.

```
Navegador (React SPA)
   │  fetch /api/*, cookies HttpOnly
   ▼
cloudmesh (Go, puerto 8080)
   ├─ http/        handlers, middleware (auth, logging, límites de tamaño)
   ├─ auth/        usuarios, hash argon2id, sesiones en SQLite
   ├─ accounts/    cuentas conectadas, estado, cuota, credenciales cifradas
   ├─ vfs/         índice del drive virtual (carpetas, archivos, búsqueda)
   ├─ transfer/    subida/descarga en streaming, enrutado por política
   ├─ sync/        reconciliación periódica índice ↔ proveedor
   ├─ storage/     interfaz Provider + storage/mega
   ├─ crypto/      AES-GCM para secretos
   └─ db/          sqlc, migraciones goose, SQLite en /data/cloudmesh.db
   │
   ▼
Mega API (una sesión viva por cuenta, cacheada en memoria)
```

### Contrato `storage`

Se separó en dos: `Provider` abre sesiones (`Login` con contraseña y 2FA, `Resume` con credenciales guardadas) y `Session` es la cuenta viva. `Session` crece por fases; en la 3 tiene `Email`, `Credentials`, `Quota`, `EnsureRoot`, `Close`. Las fases 4 y 5 añaden:

```go
List(ctx, parent string) ([]Entry, error)
Upload(ctx, parent, name string, size int64, r io.Reader) (Entry, error)
Download(ctx, id string, offset, length int64) (io.ReadCloser, error)
Rename(ctx, id, name string) error
Move(ctx, id, newParent string) error
Delete(ctx, id string) error
```

go-mega no permite parar su goroutine de sondeo de eventos; `Close` cierra el transporte HTTP y la goroutine queda dormida con backoff. Por eso el servicio mantiene una sola sesión viva por cuenta durante la vida del proceso.

Particularidades de Mega que fija la interfaz: `Upload` exige tamaño conocido de antemano (Mega cifra por chunks y necesita `fileSize`); el navegador lo envía en `Content-Length`. `Download` por rango se implementa calculando los chunks que cubren `[offset, offset+length)` con `ChunkLocation` y descartando el sobrante.

### Modelo de datos (SQLite)

- `users(id, email, password_hash, created_at)`
- `sessions(id, user_id, expires_at, created_at)`
- `accounts(id, provider, label, email, enc_credentials, status, priority, quota_used, quota_total, root_remote_id, last_sync_at, created_at)`
- `folders(id, parent_id, name, created_at)` — árbol virtual, `parent_id = 0` es la raíz (no existe como fila); único `(parent_id, name)`.
- `files(id, folder_id, name, size, mime, mtime, layout, data_shards, parity_shards, chunk_size, sha256, status, created_at)` — un archivo del drive. `layout` es `whole` (entero en una cuenta: archivos importados o guardados como `1+0`) o `rs` (troceado con paridad). `status`: `uploading | ready | degraded | lost`.
- `chunks(id, file_id, stripe, idx, kind, account_id, remote_id, size, sha256)` — tabla de bloques, el equivalente a la tabla de bloques de un RAID. `kind` es `data` o `parity`; `idx` va de 0 a `k+m-1` dentro de la franja. Único `(file_id, stripe, idx)` y `(account_id, remote_id)`. Al borrar una cuenta sus bloques quedan huérfanos (no se borran en cascada): es lo que el rebuild usa para saber qué regenerar.
- `settings(key, value)` — política de enrutado, límite de subida.
- `transfers(id, user_id, file_name, size, state, error, started_at, finished_at)` — historial corto de subidas, útil para depurar.

`enc_credentials` para Mega: JSON `{session_id, master_key}` cifrado con AES-GCM. Se obtienen con `GetSessionID()` y `GetMasterKey()` tras el login inicial; se reutilizan con `LoginWithKeys`.

### Flujos clave

Conectar cuenta: el usuario mete email, contraseña y opcionalmente código 2FA → `Login` o `MultiFactorLogin` → si Mega devuelve `EMFAREQUIRED` el frontend pide el código → se guardan session id y master key cifrados → `GetQuota` → `EnsureRoot` crea `cloudmesh/` → se dispara sync inicial.

Subir: `POST /api/files?folder=<id>&name=<nombre>` con cuerpo crudo y `Content-Length` → `transfer` decide el layout: `rs` con los `k+m` de ajustes si hay cuentas suficientes habilitadas y activas, si no `whole`. Para `rs`: lee del cuerpo una franja (`k × chunk_size` bytes, última franja más corta con relleno a cero), calcula `m` bloques de paridad, elige `k+m` cuentas distintas según la política y sube los `k+m` bloques en paralelo (cada uno un `NewUpload` de tamaño conocido) bajo `cloudmesh/blocks/<xx>/` (`xx` = `file_id % 256` en hexadecimal, para que ninguna carpeta acumule todos los bloques) con nombre `<file_id>-<stripe>-<idx>.cmb`; los bloques sueltos en `blocks/` de antes de las subcarpetas los recoloca el scrub, 500 por cuenta y pasada, sin cambiar su id remoto; inserta las filas de `chunks` conforme terminan. Al acabar marca `files.status = ready`. Si falla a medias, borra los bloques subidos y la fila. Progreso en el navegador desde `XMLHttpRequest.upload.onprogress`. Límite por archivo configurable (defecto 5 GiB) y límite de subidas concurrentes por proceso (defecto 4). Memoria por subida: una franja (`k × chunk_size`).

Descargar / preview: `GET /api/files/{id}/content` con `Range` opcional → se calculan las franjas que cubren el rango → por franja se descargan los `k` bloques de datos en paralelo; si alguno falla o falta se descargan los de paridad necesarios y se reconstruye → se recorta al rango y se escribe al response con `Content-Type`, `Content-Length`, `Accept-Ranges`. Un archivo `whole` es el caso de una sola franja con `k = 1`.

Renombrar y mover son solo índice: los bloques no tienen nombre significativo en Mega. Eliminar borra las filas y destruye los bloques en cada cuenta (sin pasar por la papelera, porque fuera de CloudMesh no sirven); los archivos `whole` importados van a la papelera de Mega.

Integridad: cada bloque lleva su SHA-256 en `chunks`; al descargar se verifica y un bloque corrupto se trata como ausente. El archivo guarda el SHA-256 del contenido completo para verificación de extremo a extremo.

Sync: cada 15 min por cuenta y bajo demanda. Lista recursiva bajo `cloudmesh/` en Mega saltando `cloudmesh/blocks/` (las subcarpetas remotas se aplanan), compara con los archivos `whole` de esa cuenta: nuevos se insertan en una carpeta virtual `Importados/<cuenta>`, faltantes se eliminan del índice, renombrados o cambiados de tamaño se actualizan por `remote_id`. Las carpetas virtuales nunca se reflejan en Mega: en Mega todo vive plano bajo `cloudmesh/` con su nombre real (Mega admite nombres repetidos en una carpeta). Renombrar en CloudMesh renombra también en Mega; mover entre carpetas virtuales no toca Mega.

### Redundancia y recuperación

- Ajustes globales: `data_shards` (`k`, defecto 2), `parity_shards` (`m`, defecto 1), `chunk_size` (defecto 32 MiB). Recomendado `2+1` con 3 cuentas y `4+2` con 6. Cambiarlos solo afecta a subidas nuevas: cada archivo guarda su propio `k+m`. Un archivo de hasta 4 MiB (y nunca mayor que un bloque) se guarda como `1+m`, es decir, `m+1` copias: menos objetos en el proveedor y una sola petición para leerlo, con la misma tolerancia a perder `m` cuentas.
- Coste: espacio extra `m/k`; memoria una franja por subida en curso. Medido en Apple M (`go test ./internal/erasure -bench .`): codificar 2+1 a 9 GB/s, 4+2 a 20 GB/s, reconstruir 4+2 con dos bloques perdidos a 3,3 GB/s, SHA-256 a 3 GB/s. Despreciable frente al cifrado que go-mega ya aplica y a la red.
- Desarrollo sin Mega: con `CLOUDMESH_DEV_FAKE_PROVIDER=1` (activo por defecto en `docker-compose.yml`) existe el proveedor `fake` en memoria (contraseña `ok`, 20 GiB) para probar el RAID de extremo a extremo. Sus datos se pierden al reiniciar la API.
- Límite real: cada lectura toca `k` cuentas, así que la cuota diaria de transferencia de las cuentas gratis de Mega se consume antes.
- Scrub: comprueba presencia y tamaño de cada bloque sin descargar (el árbol de Mega está en memoria en go-mega), borra huérfanos en `blocks/` que no pertenezcan a subidas en curso y recalcula estados: `degraded` si faltan bloques pero cada franja conserva `k`, `lost` si alguna franja no. Los hashes se verifican en cada descarga; un bloque con hash erróneo se trata como ausente. Rebuild no descarga si la franja no conserva `k` bloques o no hay cuenta libre con sitio, y el mantenimiento no reintenta un archivo a medias hasta que cambien sus bloques o las cuentas. Pendiente: muestreo de hashes en el scrub.
- Rebuild: para archivos `degraded`, regenera los bloques que faltan a partir de `k` presentes y los sube a cuentas que no tengan ya un bloque de esa franja. Se dispara al quitar una cuenta, en cada pasada de mantenimiento y a mano. Sin cuenta libre queda `pending` hasta que se conecte otra.
- Manifiesto: `users`, `accounts` (credenciales ya cifradas), `settings`, `folders`, `files` y `chunks` serializados, cifrados con la clave maestra y subidos a `cloudmesh/manifest.json.enc` de cada cuenta activa en cada pasada de mantenimiento, solo si el hash del índice cambió. Restaurar exige base vacía; las cuentas quedan en `reconnect_required` salvo la usada para restaurar.

### API REST (resumen)

```
GET    /api/setup  (¿falta admin?)  POST /api/setup  {code,email,password}
POST   /api/auth/login              POST /api/auth/logout        GET /api/auth/me
GET    /api/accounts                POST /api/accounts {…, contrib_mode?, contrib_value?}   DELETE /api/accounts/{id}
POST   /api/accounts/test           (credenciales sin dar de alta; el alta reutiliza esa sesión 5 min)
POST   /api/accounts/{id}/sync      PATCH /api/accounts/{id}     (label, priority, enabled, contrib_mode, contrib_value)
GET    /api/quota                   (agregada y por cuenta)
GET    /api/folders/{id}  (0 = raíz)  POST /api/folders      PATCH/DELETE /api/folders/{id}
POST   /api/files?folder=&name=     GET /api/files/{id}/content  PATCH/DELETE /api/files/{id}
GET    /api/files/{id}              (detalle con bloques por cuenta)
GET    /api/search?q=
GET    /api/settings                PATCH /api/settings
GET    /api/users  POST /api/users {email,password}  DELETE /api/users/{id} (no a uno mismo)
POST   /api/auth/login {email,password,code?}  (428 totp_required si falta el código)
POST   /api/auth/2fa/setup | /enable {code} | /disable {password,code}   DELETE /api/users/{id}/2fa (a otro usuario)
PATCH  /api/auth/me {name}   POST /api/auth/password {current_password,new_password}  GET/DELETE /api/auth/sessions
GET    /api/trash  DELETE /api/trash (vaciar)  POST /api/trash/{id}/restore  DELETE /api/trash/{id}
GET    /api/s3   GET/POST /api/s3/buckets   DELETE /api/s3/buckets/{id} (solo desvincula)   GET/POST /api/s3/keys   DELETE /api/s3/keys/{id}
GET    /api/health/manifest/export  (índice cifrado; `cloudmesh restore --file` lo restaura)
GET    /api/shares  POST /api/shares {kind,target_id,password?}  PATCH/DELETE /api/shares/{id}
GET    /api/public/shares/{token}   POST …/unlock {password}   GET …/folders/{id}   GET …/files/{id}/content   GET …/zip?folder=
GET    /api/healthz
```

Errores en JSON `{error: {code, message}}`. Toda ruta salvo login, healthz y `/api/public/*` exige sesión.

## 5. Frontend

Estructura `web/src/`: `routes/` (TanStack Router, file-based), `features/{auth,accounts,drive,uploads,settings}`, `components/ui` (shadcn), `lib/api.ts` (fetch tipado con zod), `lib/query.ts`.

Pantallas: login; drive (árbol de carpetas en sidebar colapsable en móvil, listado en tabla en desktop y en tarjetas en móvil, breadcrumbs, acciones por fila, drag and drop de archivos para subir); cuentas (lista con barra de cuota, botón conectar, modal con paso 2FA, reconectar); cuota agregada; ajustes (política de enrutado, límite de subida); panel de subidas fijo abajo a la derecha, ancho fluido en móvil.

Reglas: mobile-first, breakpoints `sm/md/lg`, tablas con `overflow-x-auto`, nada de anchos fijos. Verificar cada vista a 375px antes de darla por terminada.

## 6. Repositorio

```
CloudMesh/
├─ cmd/cloudmesh/main.go          arranque: config, migraciones, servidor
├─ internal/                      paquetes de la sección 4
├─ internal/db/migrations/*.sql   goose
├─ internal/db/queries/*.sql      sqlc
├─ web/                           Vite + React; `pnpm build` deja dist/ que Go embebe
├─ Dockerfile                     multi-stage: node:24-alpine → golang:1.27-alpine → distroless/static
├─ docker-compose.yml             desarrollo en contenedores (api + web)
├─ docker-compose.dokploy.yml     producción
├─ docker-compose.prod-local.yml  complemento para probar la imagen de producción en local
├─ docker/                        imagen y entrypoint de desarrollo
├─ .github/workflows/ci.yml       gofmt, vet, tests, build del front y de la imagen
├─ Makefile                       dev, build, test, lint, sqlc, docker
├─ .env.example
├─ AGENTS.md                      convenciones para agentes
└─ PLAN.md
```

Desarrollo: todo en Docker, nada instalado en la máquina. `make dev` (= `docker compose up --build`) levanta `api` (imagen `docker/api.dev.Dockerfile`, Go + air con sondeo, código montado, módulos y caché en volúmenes) y `web` (node:24 con pnpm vía corepack, Vite con HMR y proxy de `/api` al servicio `api`). Las claves de desarrollo las genera `docker/dev-entrypoint.sh` una vez y quedan en el volumen `dev_data` junto a la base. El Makefile es un envoltorio de `docker compose` (tests, sqlc, shadcn, shells). Se eligió Makefile en lugar de Taskfile porque no requiere instalar nada. En producción solo existe el 8080.

## 7. Docker y Dokploy

Imagen única. Etapa 1 compila el frontend, etapa 2 compila Go con `CGO_ENABLED=0` embebiendo `web/dist`, etapa final `gcr.io/distroless/static` con el binario y usuario no root. Tamaño esperado: menos de 40 MB.

`docker-compose.dokploy.yml`:

```yaml
services:
  app:
    build: .
    restart: unless-stopped
    environment:
      CLOUDMESH_ADDR: ":8080"
      CLOUDMESH_DATA_DIR: /data
      CLOUDMESH_MASTER_KEY: ${CLOUDMESH_MASTER_KEY:?32 bytes base64}
      CLOUDMESH_SESSION_SECRET: ${CLOUDMESH_SESSION_SECRET:?}
      CLOUDMESH_MAX_UPLOAD_BYTES: ${CLOUDMESH_MAX_UPLOAD_BYTES:-5368709120}
    volumes:
      - cloudmesh_data:/data
    expose:
      - "8080"
    networks:
      default: {}
      dokploy-network:
        aliases: [cloudmesh-app]
    healthcheck:
      test: ["CMD", "/cloudmesh", "healthcheck"]
      interval: 10s
      retries: 5

volumes:
  cloudmesh_data:

networks:
  dokploy-network:
    external: true
```

Notas Dokploy: sin `ports`, el dominio se asigna en la UI apuntando al puerto 8080 del servicio; variables en la pestaña Environment; el alias `cloudmesh-app` evita que otro stack con un servicio `app` capture el tráfico en la red compartida. Subidas grandes: subir el timeout y el tamaño máximo de cuerpo en el Traefik de Dokploy (middleware `buffering` desactivado o `maxRequestBodyBytes` alto). Backup: snapshot del volumen `cloudmesh_data`; SQLite en modo WAL con `PRAGMA busy_timeout`.

## 8. Fases

1. **Esqueleto** (hecha): módulo Go, SQLite + goose + sqlc, servidor con `/api/healthz`, Vite + React + Tailwind + shadcn, embed del dist, Dockerfile, Makefile. Entrega: contenedor que arranca y sirve la SPA vacía.
2. **Auth** (hecha): usuarios, página `/setup` protegida por código en el log para el primer administrador, `cloudmesh create-user EMAIL` (contraseña por stdin) para los demás, login y logout por cookie HttpOnly SameSite=Lax, sesiones en SQLite guardadas como HMAC(secret, token), argon2id, límite de 10 intentos de login por IP y minuto, middleware, pantalla de login con redirección a la ruta pedida.
3. **Cuentas Mega** (hecha): contrato `storage.Provider`/`storage.Session`, adaptador `storage/mega` sobre go-mega, proveedor `storage/fake` para tests, cifrado AES-GCM en `internal/crypto`, servicio `accounts` con cache de sesiones vivas y estado `reconnect_required`, endpoints `/api/accounts` y `/api/quota`, pantalla de cuentas con conexión (paso 2FA), ajustes, reconexión, borrado y actualización de cuota. Pendiente de probar con una cuenta Mega real: el adaptador solo se verificó en el camino de error (credenciales falsas contra la API real).
4. **Drive virtual y sync** (hecha): tablas `folders` y `files`, paquete `vfs` (carpetas con breadcrumbs, renombrar y mover con detección de ciclos, borrar solo vacías, búsqueda), paquete `sync` (recorrido recursivo bajo `cloudmesh/`, altas en `Importados/<cuenta>`, actualizaciones por `remote_id`, bajas, recreación de la raíz si desapareció, pasada cada 15 min y tras conectar o reconectar, una pasada a la vez por cuenta), endpoints `/api/folders`, `/api/search` y `/api/accounts/{id}/sync` con resultado, pantalla de drive con migas, búsqueda global, crear, renombrar y eliminar carpetas, filas de archivos con cuenta, tamaño y fecha.
5. **Transferencias por bloques** (hecha): `Upload`, `Download` y `Delete` en el contrato `storage`; paquete `erasure` (Reed–Solomon + benchmark para medir en el servidor real); tabla `chunks`; servicio `transfer` con subida en franjas, paridad, enrutado de `k+m` cuentas, descarga con reconstrucción y `Range`, borrado; ajustes `k+m` y tamaño de bloque; renombrar, mover y eliminar archivos; frontend con subida por arrastre y botón, panel de progreso, descarga, previews (imagen, vídeo, audio, PDF, texto) y página de ajustes.
6. **Recuperación** (hecha): `chunks.account_id` pasa a nullable con `ON DELETE SET NULL` y gana `missing`/`checked_at`; scrub por cuenta (presencia y tamaño sin descargar, borrado de huérfanos en `blocks/`, recomputo de estado `ready | degraded | lost`); rebuild por archivo (regenera con Reed–Solomon y recoloca en cuentas sin bloque de esa franja; queda pendiente si no hay cuenta libre); borrar una cuenta marca sus bloques y lanza la reparación; manifiesto cifrado `cloudmesh/manifest.json.enc` en cada cuenta activa, reescrito solo cuando el índice cambia; `cloudmesh restore PROVIDER EMAIL [MFA]` para reconstruir una instancia vacía; runner de mantenimiento cada 15 min (sync, scrub, rebuild, manifiesto); endpoints `/api/health*` y `/api/files/{id}/rebuild`; pantalla «Salud».
7. **Producción** (hecha): `IdleTimeout` y `MaxHeaderBytes` en el servidor; las transferencias renuevan su plazo de lectura o escritura en cada operación (2 min sin datos corta la conexión, una transferencia lenta pero viva no); semáforo de descargas además del de subidas (`CLOUDMESH_MAX_UPLOADS`, `CLOUDMESH_MAX_DOWNLOADS`); `CLOUDMESH_TRUST_PROXY` para fiarse de `X-Forwarded-*` solo detrás de Traefik; cabeceras defensivas; versión por `ldflags` visible en `/api/healthz`; `cloudmesh backup` con `VACUUM INTO` y copia local diaria rotando 7; `Encode` sin copia para franjas completas y `GOGC=50` en la imagen. Prueba local con Traefik v3.6 delante de la imagen de producción: el `readTimeout` por defecto de 60 s corta una subida de 1 GiB a 12 MB/s (499 a los 61 s) y con 4 h termina en 85 s con SHA-256 correcto; memoria de la app 565 MiB de pico en la subida y 330 MiB en la descarga antes de las dos últimas mejoras. Documentado en README cómo ajustar el Traefik de Dokploy.
8. **Rediseño y enlaces** (hecha el 2026-10-21): UI nueva a partir del diseño del canvas (sidebar, barra inferior en móvil, tarjetas en lugar de tablas en pantallas pequeñas) con vistas Mi espacio, Enlaces compartidos, Usuarios, Almacenamiento (cuentas, redundancia con simulación, verificación, reparación y respaldo del índice), Perfil (contraseña, sesiones, política de enrutado) y Buckets S3 como «próximamente». Backend: tabla `shares` y paquete `shares` (enlaces servidos por CloudMesh, no por `Link` de Mega, para que funcionen con archivos troceados); páginas públicas `/s/{token}` con contraseña (cookie HMAC por enlace), caducidad, límite de descargas (un `Range` que no empieza en 0 no cuenta) y ZIP en streaming sin compresión; HTML/SVG/XML se fuerzan como descarga en enlaces públicos. Usuarios: alta, baja y cambio de contraseña desde la UI; cerrar las demás sesiones. Los enlaces no entran en el manifiesto: tras un `restore` hay que recrearlos. Después: nombre visible del usuario (columna `users.name`, sí va en el manifiesto), exportar el índice cifrado desde Almacenamiento y restaurarlo con `cloudmesh restore --file`, barra de espacio en Mi espacio y miniaturas de imágenes en el enlace público solo si no tiene límite de descargas (cada miniatura cuenta como descarga).
   Papelera: `DELETE /api/files/{id}` marca `files.deleted_at`; el archivo desaparece del disco, la búsqueda y los enlaces pero conserva sus bloques (ocupa espacio, se verifica y repara). Restaurar lo devuelve a su carpeta o a la raíz si ya no existe; las carpetas con solo archivos en la papelera se pueden borrar. El mantenimiento purga lo que lleva más de 30 días. Rutas del frontend en inglés (`/shares`, `/users`, `/storage`, `/trash`, `/profile`). Aporte por cuenta (`contrib_mode` all | reserve | limit): es un techo de llenado sobre lo usado en la cuenta (deja un % libre o no pasa de N bytes); `Account.Capacity` lo aplica y lo usan el enrutado, la reparación y la cuota del disco. «Probar conexión» abre la sesión sin guardar la cuenta y la deja 5 minutos para que el alta no repita el código 2FA. El alta en Almacenamiento va en tres pasos (proveedor, acceso, ajustes). Verificación en dos pasos TOTP para entrar (RFC 6238, implementada con la stdlib): secreto cifrado con la clave maestra (va en el manifiesto y sobrevive a un restore), un código no se acepta dos veces, activarla cierra las demás sesiones, desactivarla pide contraseña y código; si alguien pierde el móvil la quita otro usuario desde Usuarios o `cloudmesh reset-2fa EMAIL`. El QR se dibuja en el navegador con `uqr`. Los códigos de recuperación llegaron en la fase 13.
   Decisión: los enlaces son de solo lectura, como en Mega, con contraseña opcional. Descartadas las subidas por enlace público (carpetas de subida): las subidas desde fuera de la app irán solo por la API S3.
   Decidido: los enlaces quedan como en Mega, solo lectura y contraseña opcional; la caducidad y el límite de descargas se quitaron (migración 0015). El contador de descargas se mantiene como dato.
9. **API S3 estilo R2** (hecha el 2026-10-21): paquete `s3` en un segundo puerto (`CLOUDMESH_S3_ADDR`, defecto `:9000`). Buckets que apuntan a carpetas del disco; llaves de acceso con los permisos de R2 (admin o objetos, lectura o lectura/escritura, opcionalmente por bucket) y secreto cifrado con la clave maestra; SigV4 en cabecera, prefirmada y `aws-chunked`; ListObjects v1/v2, Get/Head con Range, Put, Copy, Delete(Objects), multipart (objetos compuestos de partes internas, sin copiar datos; abandonadas se limpian a los 7 días) y metadatos `x-amz-meta-*`. ETag MD5 en las subidas nuevas. Sobrescribir o borrar por S3 manda a la papelera y quita las carpetas que quedan vacías. Buckets, llaves y partes van en el manifiesto. Probado con aws-cli 2.27 (cp, multipart, sync, presign, rm, rb) y rclone 1.69 (sync, check por MD5, check --download, purge).
10. **Roles y cuota por usuario** (hecha el 2026-10-21): migración 0016 (`users.role`, `users.quota_bytes`, `files.owner_id`). Tres roles globales: administrador (todo: cuentas, usuarios, ajustes, buckets S3, mantenimiento, vaciar papelera), editor (subir, carpetas, renombrar, mover, borrar a la papelera, restaurar, enlaces) y lector (ver, buscar, descargar). Los usuarios que ya existían quedan como administradores; los nuevos desde la web, editores por defecto; `create-user` y `/setup` crean administradores. No se puede quitar ni borrar al último administrador. Cuota en bytes por usuario (0 = sin límite) sobre lo que sube, papelera incluida; por S3 se carga a quien creó la llave; los archivos anteriores no tienen dueño y no cuentan. La comprobación va bajo el mismo cerrojo que reserva el espacio, así dos subidas a la vez no la saltan. Sin rol «solo S3»: para eso están las llaves. La UI oculta lo que el rol no permite y las rutas lo comprueban en `beforeLoad`.
11. **Extras de R2 en la API S3** (hecha el 2026-10-21): migración 0017 (`s3_buckets.public`, `cors`, `lifecycle`). Estilo virtual-host con el host de `CLOUDMESH_S3_URL` (necesita DNS comodín; sin variable propia). Bucket público: GET/HEAD de objetos sin firma, como el dominio público de R2; listar sigue pidiendo llave. CORS por bucket en el formato de R2, con preflight sin firma y cabeceras también en errores. Ciclo de vida: expirar por prefijo a los N días desde la subida (a la papelera) y cancelar multipart a medias; sin transiciones (no hay clases de almacenamiento) ni filtros por etiqueta. `UploadPartCopy` con `x-amz-copy-source-range`. `GetObjectTagging` vacío para que `aws s3 cp` entre buckets funcione con objetos grandes. Configurable desde la app y desde la API con llaves Admin.
12. **Cifrado de bloques y límite de transferencia** (hecha el 2026-10-21): migración 0018 (`files.enc_key`, `chunks.encrypted`). Cifrado en el servidor, no en el navegador: los proveedores solo ven datos cifrados y siguen funcionando S3, enlaces, vistas previas y la reparación desatendida. Cada archivo tiene una clave AES-256 aleatoria envuelta con `CLOUDMESH_MASTER_KEY` (viaja en el manifiesto, que ya va cifrado con ella); cada bloque se sella con AES-256-GCM, nonce derivado de (franja, índice) y AAD que ata archivo y posición. `chunks.size`/`sha256` son siempre los del bloque tal como está en el proveedor, así el scrub no cambia. Las subidas ya no se guardan enteras: con menos cuentas que `k+m` van en bloques 1+0 (cifrados, sin paridad); `whole` queda solo para lo que el sync importa del proveedor, que no se cifra. Los bloques antiguos los cifra el mantenimiento, 16 por pasada (descarga, sella, sube a la misma cuenta y destruye el original, bloque a bloque; un archivo a medio convertir se sigue leyendo). El scrub respeta una hora los bloques que el índice aún no conoce, para no borrar lo que una reparación o el cifrado acaban de subir.
   Límite de transferencia: `storage.ErrTransferQuota` (Mega: `EOVERQUOTA` al pedir la descarga o HTTP 509 a mitad). La cuenta queda apartada una hora en memoria (no va al índice ni al manifiesto); las lecturas reúnen primero bloques de datos y paridad de cuentas no limitadas y solo como último recurso tocan las limitadas; el enrutado de subidas las deja al final. La app muestra «Transferencia agotada» en la cuenta.
13. **Usuarios y acceso** (hecha el 2026-10-21):
   - Códigos de recuperación de la 2FA (migración 0019): diez de un solo uso al activarla, con su SHA-256 en `users.recovery_codes`. Sirven para entrar y para desactivar la 2FA si se pierde el móvil, y se regeneran desde el perfil con contraseña y código.
   - Invitaciones (0020): enlace de un solo uso que caduca a los 7 días; se guarda el SHA-256 del token. Quien invita elige el rol y la cuota. Con `CLOUDMESH_SMTP_URL` (smtp:// con STARTTLS o smtps://) el enlace también va por correo con `net/smtp`; sin SMTP se copia. Aceptarla crea la cuenta y abre la sesión.
   - Carpetas restringidas (0021): `folders.restricted` y `folder_access` (lectura o edición por usuario). Una carpeta restringida y todo lo que cuelga de ella solo la ven los administradores y los usuarios de su lista; sin restricciones todo sigue como un disco común. Se aplica en listados, búsqueda, contenido, subidas, mover, renombrar, borrar, papelera, enlaces y salud; un enlace público de una carpeta superior no muestra ni deja entrar en las restringidas. Las llaves S3 no se ven afectadas (solo las crean administradores).
   - Arreglos: el manifiesto ya no incluye `totp_last_step`, que cambia en cada entrada con 2FA, y cambiar el nombre propio ya no deja el rol vacío en la app.
14. (Después) Segundo proveedor.

Cada fase termina con tests Go de los paquetes tocados y un smoke test manual en el contenedor.

## 9. Riesgos y verificaciones pendientes

- **go-mega**: cliente comunitario, último commit julio 2026. Es el que usa rclone, así que está probado, pero no es oficial. Mitigación: la interfaz `Provider` aísla la dependencia.
- **Sesiones Mega**: no está confirmado cuánto dura una sesión reutilizada con `LoginWithKeys` ni si Mega la invalida al cambiar contraseña. Comprobar en fase 3; el estado `reconnect_required` cubre el caso.
- **Cuotas de transferencia de Mega** (tratado en la fase 12): las cuentas gratuitas tienen límite de ancho de banda, no solo de almacenamiento. Mega no dice cuándo se renueva, así que la cuenta se aparta una hora y se vuelve a probar.
- **Tamaño conocido de antemano**: Mega no admite subida de tamaño desconocido. La API exige `Content-Length`; sin él se rechaza con 411.
- **Memoria en subidas concurrentes**: cada subida mantiene en memoria una franja (`k × chunk_size`, por defecto 64 MiB). Limitar concurrencia por proceso a un valor configurable (defecto 4).
- **Índice como punto único de fallo**: sin SQLite los bloques son basura. El manifiesto cifrado en cada cuenta lo cubre (ventana máxima de 15 min entre el último cambio y su copia); backup del volumen como segunda línea.
- **Archivos ilegibles desde Mega**: los bloques solo los entiende CloudMesh. Es el precio del RAID. Los archivos importados (`whole`) siguen siendo normales.
- **Términos de servicio de Mega**: varias cuentas gratuitas agregadas pueden ir contra sus términos. Uso interno, decisión del usuario; dejarlo anotado.
- **Traefik y cuerpos grandes**: en la fase 7 se validó 1 GiB con Traefik v3.6 en local; el 2026-10-21 se repitió con 2,5 GiB a 25 MB/s: con el `readTimeout` por defecto muere a los 60 s (504) sin dejar rastro en el índice; con 4 h sube en 103 s (pico de 545 MiB en la app) y la descarga, también de más de 60 s, coincide en SHA-256. Falta repetirlo contra el Traefik de Dokploy en el servidor real.
