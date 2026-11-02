# Despliegue (VPS)

Mismo esquema que Orbita: imagen Docker construida en GitHub Actions → GHCR → el VPS la
descarga por SSH con `docker compose`. El servidor nunca compila.

| | API |
|---|---|
| Repo | `atm-messaging-api` |
| Dominio | `messaging.atmosferast.com` |
| Carpeta en el VPS | `/opt/messaging-api` |
| Contenedor | `messaging-api` (puerto interno 8080) |
| Puerto en loopback | `127.0.0.1:3121` |
| Imagen | `ghcr.io/atmosferasoltec/atm-messaging-api` |

El nginx del host publica el dominio con el certificado de origen de Cloudflare
(`/etc/ssl/atmosferast/origin.pem`, cubre `*.atmosferast.com`). La consola (`console.atmosferast.com`)
no usa este dominio: su nginx reenvía `/messaging-api/` a `127.0.0.1:3121`, mismo origen y sin CORS.
Ya no hay web propia (`atm-messaging-web` se retiró): se administra desde la consola.

## Cómo se despliega

1. Se trabaja en `main`.
2. Cuando está listo, se lleva a `production` (`git checkout production && git merge main && git push`).
3. El push a `production` dispara el workflow `Deploy` de cada repo: **check** (lint, tests, build)
   → **build** (imagen a GHCR) → **deploy** (SSH, `docker compose pull && up -d`, espera a que el
   contenedor quede `healthy`). También se puede relanzar a mano desde Actions (`workflow_dispatch`),
   por ejemplo para volver a una versión anterior.

Secretos del repositorio (los mismos nombres que en los demás proyectos): `SSH_HOST`, `SSH_USER`,
`SSH_KEY`. El login a GHCR vive en el servidor (`~/.docker/config.json`); si el `pull` dice
`denied`, renovarlo con `docker login ghcr.io` en el VPS.

El `docker-compose.yml` de `deploy/` se copia al servidor en cada despliegue: no se edita allá.
En cada despliegue, un servicio `migrate` aplica las migraciones pendientes **antes** de arrancar
la app, y una migración rota bloquea el despliegue. Por eso las migraciones deben ser compatibles
con la versión anterior mientras corre (primero agregar, después quitar).

## Preparación del servidor (hecha una vez, el 2026-10-28)

- **Base de datos**: rol `messaging_app`, base `messaging`, esquema `messaging`, confinados como los
  demás proyectos (`scripts/provision-db.sql`; el rol no puede usar `public` ni conectarse a otras
  bases, y ningún otro rol puede conectarse a esta). La clave se generó al crearla y está solo en
  el `.env` del servidor.
- **Carpeta** `/opt/messaging-api` (dueño `deploy`).
- **nginx del host**: `deploy/host-nginx/` tiene los dos vhosts y el `map` de la IP real del visitante
  (Cloudflare). Se instalan en `/etc/nginx/sites-available` (+ symlink en `sites-enabled`) y
  `/etc/nginx/conf.d`. El vhost del API desactiva el buffering de los streams SSE del QR.
- **`/opt/messaging-api/.env`**: lo crea el operador a mano (el despliegue falla con un mensaje claro
  si no existe). Plantilla en `deploy/.env.production.example`:

  ```bash
  install -m 600 -o deploy -g deploy /dev/null /opt/messaging-api/.env
  nano /opt/messaging-api/.env
  ```

## Correo transaccional

`POST /v1/email` envía correo con Resend desde una cola en la base (tabla `email_messages`). En el `.env`
del servidor: `RESEND_API_KEY` y `MAIL_FROM` (dominio verificado en Resend, p. ej.
`Atmosfera <no-reply@atmosferast.com>`). Si falta `RESEND_API_KEY` el servicio arranca igual y WhatsApp
funciona, pero `POST /v1/email` responde 503 `email_unavailable`. Reintentos: `EMAIL_MAX_ATTEMPTS` (5) y
`EMAIL_RETRY_BASE` (30s, se duplica). Los códigos de recuperación se borran de la base al enviarse o al
fallar definitivamente. Si el contenedor se detiene, los correos pendientes se envían al volver a arrancar.

## Después del primer despliegue

```bash
docker ps --filter name=messaging              # ambos "healthy"
curl https://messaging.atmosferast.com/health   # {"status":"ok"}
```

Entrar a https://console.atmosferast.com como superadmin, abrir Mensajería, crear una conexión
y probar el link de vinculación. Los sistemas aparecen solos la primera vez que llaman con su token.

## Notas operativas

- **Una sola instancia.** El servidor toma un lock de PostgreSQL al arrancar y se niega a iniciar si
  otro ya corre sobre la misma base: un dispositivo de WhatsApp solo puede estar conectado en un
  lugar. Nunca apuntes una copia (pruebas, local) a la base de producción.
- **Las sesiones de WhatsApp viven en la base** (tablas `whatsmeow_*` del esquema `messaging`) y se
  restauran solas al reiniciar el contenedor. **Los respaldos de esa base contienen credenciales de
  los clientes: trátalos como sensibles.**
- Al parar el contenedor (`SIGTERM`) el servidor termina el envío en curso y cierra las conexiones
  (hasta 30 s); lo que quede en la cola se retoma al volver a arrancar.
- Los webhooks de los sistemas solo aceptan `https` y nunca llegan a direcciones privadas
  (`WEBHOOK_ALLOW_PRIVATE` debe seguir apagado en producción).
- Si Cloudflare corta los streams del QR (límite de ~100 s sin datos), el cliente verá
  "El código venció" y podrá pedir otro desde la misma página.
