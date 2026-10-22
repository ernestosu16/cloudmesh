# CloudMesh

[English](README.md) · **Español**

Varias nubes, un solo drive, al estilo de un RAID sobre nubes. CloudMesh conecta varias cuentas de almacenamiento (Mega en v1) y las convierte en un único drive autoalojado: cuota agregada, carpetas virtuales, y cada archivo troceado en bloques repartidos entre cuentas con paridad Reed–Solomon configurable (`k+m`), de modo que perder hasta `m` cuentas no pierde datos. Cada bloque sale del servidor cifrado con AES-256-GCM y una clave por archivo: los proveedores solo ven datos cifrados.

Go + React 19 en un solo contenedor, SQLite, sin servicios externos.

> [!WARNING]
> CloudMesh se ofrece tal cual, sin garantía de ningún tipo. **Cada persona que lo usa es la única responsable del uso que hace de él**: de cumplir los términos de servicio de cada proveedor que conecta, las leyes que le aplican, y de los datos que guarda. Lee el [aviso de responsabilidad](#aviso-de-responsabilidad) antes de desplegarlo.

![Mi espacio](docs/screenshots/drive.png)

| | |
|---|---|
| ![Almacenamiento](docs/screenshots/storage.png) | ![Buckets S3](docs/screenshots/s3.png) |
| **Almacenamiento**: cuentas conectadas, capacidad agregada y estado de la redundancia | **API S3**: buckets que apuntan a carpetas del drive, llaves al estilo R2 |
| ![Carpeta compartida](docs/screenshots/public-share.png) | ![Tema oscuro](docs/screenshots/drive-dark.png) |
| **Enlaces públicos**: páginas de solo lectura, contraseña opcional, descarga en ZIP | **Tema claro y oscuro**, interfaz en español e inglés |
| ![Usuarios y roles](docs/screenshots/users.png) | <img src="docs/screenshots/mobile-drive.png" alt="Vista móvil" width="260"> |
| **Usuarios**: roles, cuota por usuario, invitaciones y 2FA | **Pensado para móvil**, instalable como webapp |

Las capturas usan datos de demostración y el proveedor `fake` de desarrollo.

## Qué hace

- **Un drive sobre muchas cuentas**: sus cuotas se suman; quien usa CloudMesh solo ve carpetas y archivos.
- **Codificación con borrado**: cada archivo se corta en franjas de `k` bloques de datos más `m` de paridad (Reed–Solomon), cada uno en una cuenta distinta. Los archivos pequeños se guardan como copias cifradas en vez de franjas. Cada archivo guarda su propio `k+m`; cambiar los ajustes solo afecta a subidas nuevas.
- **Bloques cifrados**: AES-256-GCM, una clave por archivo envuelta con tu clave maestra. Sin esa clave, lo que hay en los proveedores no se puede leer.
- **Autorreparación**: cada 15 minutos una pasada de mantenimiento sincroniza con los proveedores, comprueba que todos los bloques siguen ahí, regenera con la paridad los que falten y guarda una copia cifrada del índice en cada cuenta.
- **Recuperación ante desastre**: reconstruye el servidor entero con cualquier cuenta viva y la clave maestra.
- **Subidas y descargas en streaming** con `Range` para previsualizar vídeo y audio, una pequeña caché en memoria para los visores y una cola de subidas que acepta carpetas enteras.
- **Previsualización** de imágenes, vídeo, audio, PDF y texto; búsqueda; renombrar y mover; papelera con 30 días de retención.
- **Enlaces públicos** a archivos o carpetas, de solo lectura, con contraseña opcional y contador de descargas.
- **API compatible con S3** al estilo de Cloudflare R2: SigV4, URL prefirmadas, multipart, buckets públicos, CORS y ciclo de vida. Funciona con `aws`, `rclone`, `restic` y compañía.
- **Multiusuario**: roles administrador / editor / lector, cuota por usuario, invitaciones por enlace o correo, carpetas restringidas, verificación en dos pasos TOTP con códigos de recuperación.
- **Un solo binario estático** con el frontend embebido, base SQLite, imagen distroless. Webapp instalable, interfaz bilingüe (español / inglés), tema claro y oscuro.

## Cómo funciona

```
navegador / cliente S3
        │
        ▼
  CloudMesh (Go)  ── índice SQLite: usuarios, carpetas, archivos, bloques
        │
        │  franjas · Reed–Solomon k+m · AES-256-GCM por bloque
        ▼
 ┌──────────┬──────────┬──────────┐
 │ cuenta 1 │ cuenta 2 │ cuenta 3 │   …   (Mega en v1)
 │  datos   │  datos   │ paridad  │
 └──────────┴──────────┴──────────┘
```

Con el `2+1` por defecto, cualquier cuenta puede desaparecer (borrada, suspendida, sin cuota de transferencia) y todos los archivos se siguen leyendo y reparando. Las subidas se enrutan por espacio libre (`most_free`, defecto), `round_robin` o `priority`. Con menos de `k+m` cuentas disponibles el archivo se guarda sin paridad (`1+0`) y la interfaz lo avisa.

Diseño, alcance y fases en [PLAN.md](PLAN.md).

## Instalación

Solo necesitas **Docker** (con Docker Compose). Go, Node y pnpm corren dentro de contenedores.

### Producción en tu propio servidor

1. Clona el repositorio y crea `.env` a partir del ejemplo:

   ```bash
   git clone https://github.com/ernestosu16/cloudmesh.git cloudmesh
   ```

   ```bash
   cd cloudmesh && cp .env.example .env
   ```

2. Genera los dos secretos obligatorios y pégalos en `.env`:

   ```bash
   openssl rand -base64 32
   ```

   ```
   CLOUDMESH_MASTER_KEY=<primer valor>
   CLOUDMESH_SESSION_SECRET=<segundo valor>
   ```

   **Guarda `CLOUDMESH_MASTER_KEY` en un sitio seguro.** Cifra las credenciales de los proveedores y las claves de cada archivo. Si se pierde, los datos guardados no se pueden descifrar y hay que reconectar todas las cuentas.

3. Construye y arranca la imagen de producción (app en el puerto `8080`, API S3 en `9000`):

   ```bash
   make prod
   ```

   Sin `make`:

   ```bash
   docker compose -f docker-compose.dokploy.yml -f docker-compose.prod-local.yml --env-file .env up --build -d
   ```

4. Busca en el log el código de configuración de un solo uso, una línea con `"setup_code":"XXXX-XXXX"`:

   ```bash
   docker compose -f docker-compose.dokploy.yml logs app
   ```

5. Abre `http://tu-servidor:8080`. Te lleva a `/setup`: pega el código y crea el administrador. Si pierdes el código, reinicia el contenedor; se genera otro mientras no haya usuarios.

6. En **Almacenamiento → Añadir cuenta**, conecta tus cuentas Mega (email, contraseña y código 2FA si lo tienen). Conecta al menos `k+m` cuentas (tres para el `2+1` por defecto) para tener redundancia.

Fuera de una red local, pon CloudMesh detrás de un proxy inverso con HTTPS (Traefik, Caddy, nginx) y define `CLOUDMESH_TRUST_PROXY=1` para que lea `X-Forwarded-For` y `X-Forwarded-Proto`. Todos los datos viven en el volumen `/data`.

### Producción con Dokploy

La imagen final es un binario estático en `distroless` con el frontend embebido, y persiste todo en el volumen `/data` (SQLite).

1. Sube el repositorio a GitHub, GitLab o Gitea.
2. En Dokploy: **Create Service → Compose**. Proveedor: tu repositorio, rama `main`, **Compose Path** `docker-compose.dokploy.yml`.
3. Pestaña **Environment**: define `CLOUDMESH_MASTER_KEY` y `CLOUDMESH_SESSION_SECRET` (`openssl rand -base64 32` cada una). `CLOUDMESH_TRUST_PROXY` ya viene a `1` en ese compose.
4. Pestaña **Domains**: añade tu dominio apuntando al servicio `app`, puerto `8080`, HTTPS con Let's Encrypt. Para la API S3, añade otro dominio (p. ej. `s3.tudominio`) al puerto `9000` y ponlo en `CLOUDMESH_S3_URL`.
5. **Deploy**; luego busca `setup_code` en **Logs**, abre el dominio y crea el administrador.

El servicio lleva el alias `cloudmesh-app` en `dokploy-network` para que otro stack con un servicio `app` no capture su tráfico.

#### Subidas grandes: el timeout de Traefik

Traefik v3, el que usa Dokploy, corta por defecto cualquier petición cuyo cuerpo tarde más de **60 segundos** en llegar (`respondingTimeouts.readTimeout`). Una subida de varios GB desde una conexión doméstica muere a mitad con un 499 o 504 aunque CloudMesh la acepte. Sube ese límite en Dokploy → **Settings → Traefik → Traefik config**, en cada entrypoint que use tu dominio (`web` y `websecure`):

```yaml
entryPoints:
  websecure:
    address: ":443"
    transport:
      respondingTimeouts:
        readTimeout: 4h
        writeTimeout: 4h
        idleTimeout: 180s
```

`4h` da margen para 5 GiB a 3 Mbit/s. Guarda y reinicia Traefik. No hace falta tocar el tamaño máximo de cuerpo: Traefik no lo limita salvo que actives el middleware `buffering`. CloudMesh limita cada archivo con `CLOUDMESH_MAX_UPLOAD_BYTES` y corta una transferencia que se quede **2 minutos sin mover datos**; una subida lenta pero viva no caduca.

Medido en local con la imagen de producción detrás de Traefik v3.6: una subida de 1 GiB a 12 MB/s muere a los 61 s con `499 Client Closed Request` usando el defecto, y termina en 85 s con `readTimeout: 4h`. Se reproduce con `docker-compose.traefik-local.yml` y `TRAEFIK_READ_TIMEOUT=60s` frente a `4h`.

### Memoria y tamaño del servidor

Cada subida en curso retiene en memoria una franja (`k × bloque`, 64 MiB con `2+1` y bloques de 32 MiB) más su paridad y el margen del recolector de Go: cuenta unos **250 MiB por subida** y **150 MiB por descarga** con los valores por defecto. Con `CLOUDMESH_MAX_UPLOADS=4` y `CLOUDMESH_MAX_DOWNLOADS=8` el peor caso ronda 2,5 GiB. En un VPS de 2 GiB baja a `CLOUDMESH_MAX_UPLOADS=2`, `CLOUDMESH_MAX_DOWNLOADS=4` o usa bloques de 16 MiB (Almacenamiento → Configurar). La CPU no es el límite: la paridad va a varios GB/s.

## Configuración

Solo los dos secretos son obligatorios; el resto tiene valor por defecto. Ver [.env.example](.env.example).

| Variable | Defecto | Descripción |
|---|---|---|
| `CLOUDMESH_MASTER_KEY` | — | 32 bytes en base64. Cifra credenciales de proveedores y claves de archivos. **Obligatoria.** |
| `CLOUDMESH_SESSION_SECRET` | — | 32 bytes en base64. Firma las cookies de sesión. **Obligatoria.** |
| `CLOUDMESH_MAX_UPLOAD_BYTES` | `5368709120` | Tamaño máximo por archivo (5 GiB). |
| `CLOUDMESH_MAX_UPLOADS` / `CLOUDMESH_MAX_DOWNLOADS` | `4` / `8` | Transferencias simultáneas por proceso. |
| `CLOUDMESH_TRUST_PROXY` | `0` | `1` detrás de un proxy inverso: confía en `X-Forwarded-*`. |
| `CLOUDMESH_S3_ADDR` | `:9000` | Dirección de la API S3; `off` la desactiva. |
| `CLOUDMESH_S3_URL` | vacía | URL pública de la API S3 que muestra la app; su host activa también el estilo virtual-host. |
| `CLOUDMESH_S3_REGION` | `auto` | Región que se anuncia. |
| `CLOUDMESH_SMTP_URL` / `CLOUDMESH_SMTP_FROM` | vacías | `smtp://usuario:clave@host:587` o `smtps://…:465` para enviar invitaciones. |
| `CLOUDMESH_LOG_LEVEL` | `info` | `debug` registra los tiempos de cada lectura de bloque. |

## Usuarios y acceso

El primer usuario es administrador. Desde **Usuarios** invitas al resto por enlace (o los creas con contraseña) con un rol —administrador, editor o lector— y, si quieres, una cuota de espacio. Con `CLOUDMESH_SMTP_URL` la invitación llega por correo. Un administrador puede restringir cualquier carpeta a ciertos usuarios (acción **Acceso** de la carpeta). Al activar la verificación en dos pasos se muestran diez códigos de recuperación de un solo uso.

## API compatible con S3

CloudMesh expone una API S3 al estilo de Cloudflare R2 en un puerto propio (`CLOUDMESH_S3_ADDR`, por defecto `:9000`).

- **Buckets**: cada uno apunta a una carpeta del drive; `a/b/c.txt` es el archivo `c.txt` en las subcarpetas `a/b`. Lo subido por S3 se ve en el drive, y lo que ya había se lee por S3. Sobrescribir o borrar un objeto lo manda a la papelera.
- **Llaves** (pantalla Buckets S3): Admin u Objetos, de lectura o de lectura/escritura; las de objetos se pueden limitar a ciertos buckets. El secreto solo se muestra al crearla.
- **Configuración por bucket**: acceso público (GET/HEAD sin firma), reglas CORS en el formato JSON de R2 y ciclo de vida (borrar los objetos de un prefijo a los N días, cancelar subidas multipart a medias).
- **Protocolo**: SigV4 (cabecera, URL prefirmada y `aws-chunked`), estilos «path» y virtual-host, región `auto`, ListObjects v1/v2, Get/Head con `Range`, Put, Copy, Delete y DeleteObjects, multipart con `UploadPartCopy`, metadatos `x-amz-meta-*`. Sin versionado, ACL, políticas de bucket, transiciones ni etiquetas.

```bash
aws --endpoint-url https://s3.tudominio s3 sync ./respaldos s3://respaldos
```

Para rclone: `type = s3`, `provider = Other`, `endpoint = https://s3.tudominio`, `region = auto`, `force_path_style = true` (o `false` con el DNS comodín `*.s3.tudominio`).

## Cifrado y límites de Mega

- **Cifrado**: las claves de los archivos van envueltas con `CLOUDMESH_MASTER_KEY`. Sin ella nada se puede leer, ni siquiera con acceso a las cuentas. Los archivos que el sync encuentra ya en la carpeta `cloudmesh` del proveedor se importan tal cual, visibles allí y sin cifrar; todo lo subido desde CloudMesh va siempre en bloques cifrados.
- **Credenciales**: nunca se guarda la contraseña del proveedor, solo su sesión reanudable cifrada con la clave maestra.
- **Límite de transferencia**: las cuentas gratuitas de Mega limitan cuánto se descarga. Cuando una lo alcanza, CloudMesh la aparta una hora: lee los archivos con la paridad de las demás y le sube lo nuevo solo si no queda otra. Un archivo sin paridad (`1+0`) guardado en esa cuenta no se puede descargar hasta que Mega renueve la cuota.

## Copias de seguridad y recuperación

Tres capas, de más a menos automática:

- **Manifiesto en las nubes**: cada 15 minutos, si el índice cambió, se guarda una copia cifrada (`cloudmesh/manifest.json.enc`) en cada cuenta.
- **Copia local de la base**: cada 24 horas el servidor escribe `/data/backups/cloudmesh-<fecha>.db` (copia consistente con `VACUUM INTO`) y conserva las 7 últimas. A mano: `docker compose exec app /cloudmesh backup`.
- **Backup del volumen** `/data` desde tu servidor o Dokploy.

`CLOUDMESH_MASTER_KEY` no está en ninguna de ellas. Guárdala aparte.

Si pierdes el servidor entero, con la misma clave maestra y una cuenta viva:

```bash
docker compose exec -i app /cloudmesh restore mega tu@email.com
```

Pide la contraseña de Mega (añade el código 2FA como tercer argumento si lo usas), descarga el manifiesto, lo descifra y reconstruye usuarios, cuentas, carpetas, archivos y bloques en la base vacía. Esa cuenta queda conectada; las demás se reconectan desde **Almacenamiento**. Con un índice exportado desde la app: `cloudmesh restore --file ARCHIVO`.

## Desarrollo

```bash
make dev
```

o, sin `make`:

```bash
docker compose up --build
```

- Frontend con recarga en caliente: http://localhost:5173 (`CLOUDMESH_WEB_PORT` en `.env`)
- API con recarga (air): http://localhost:8080
- Las claves de desarrollo se generan solas y se guardan en el volumen `dev_data`.

Saca el código de configuración con `make logs` y abre http://localhost:5173.

**Probar sin cuentas Mega**: en desarrollo está activo un proveedor `fake` en memoria. En **Almacenamiento → Añadir cuenta** elige *Fake*, cualquier email y contraseña `ok`. Conecta tres para ver el `2+1` funcionando. Se desactiva con `CLOUDMESH_DEV_FAKE_PROVIDER=0`.

| Comando | Qué hace |
|---|---|
| `make dev` / `make down` | Levanta y para el entorno |
| `make logs` | Logs de la API (aquí sale el código de configuración) |
| `make test` | Tests Go, typecheck y lint del frontend |
| `make sqlc` | Regenera el código SQL tras tocar `internal/db/queries` o `migrations` |
| `make user EMAIL=x@y.com` | Crea un usuario adicional |
| `make shadcn C="table"` | Añade componentes shadcn/ui |
| `make api-sh` / `make web-sh` | Shell dentro de cada contenedor |
| `make prod` | Construye y arranca la imagen de producción en local |
| `make clean` | Borra contenedores y volúmenes de desarrollo, base incluida |

En Windows sin `make`, usa los comandos `docker compose` equivalentes del [Makefile](Makefile).

### Estructura

```
cmd/cloudmesh/     arranque y subcomandos (serve, healthcheck, create-user, backup, restore)
internal/          auth, accounts, vfs, sync, transfer (bloques, scrub, rebuild), erasure, manifest,
                   maintenance, settings, shares, s3, storage (mega, fake), crypto, db, http
web/               Vite + React; `pnpm build` genera web/dist, que Go embebe
docker/            imagen y entrypoint de desarrollo
```

Convenciones para colaboradores y agentes en [AGENTS.md](AGENTS.md).

## Aviso de responsabilidad

CloudMesh es un proyecto independiente de código abierto. No está afiliado, respaldado ni patrocinado por Mega ni por ningún otro proveedor de almacenamiento; las marcas pertenecen a sus dueños.

El software se ofrece «tal cual», sin garantía de ningún tipo (ver [LICENSE](LICENSE)). Al usarlo aceptas que:

- **Eres el único responsable del uso que haces de él.** Los autores y colaboradores no responden de ningún mal uso, daño, pérdida de datos, suspensión de cuentas ni consecuencia legal derivada de su uso.
- **Debes cumplir los términos de servicio de cada proveedor que conectes.** Algunos proveedores limitan el número de cuentas por persona, el acceso automatizado o el uso que se puede dar a su almacenamiento. Revisa esos términos antes de conectar cuentas; CloudMesh no lo hace por ti.
- **Eres responsable del contenido que guardas y compartes** y de cumplir las leyes que te apliquen, incluidas las de propiedad intelectual y protección de datos.
- **Eres responsable de tus claves y copias de seguridad.** Perder `CLOUDMESH_MASTER_KEY` hace irrecuperables los datos guardados. La redundancia reduce el riesgo de perder datos, no lo elimina. Guarda copias independientes de todo lo importante.

## Licencia

[MIT](LICENSE) © 2026 Ernesto Suarez Ramirez
