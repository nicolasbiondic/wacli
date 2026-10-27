# wacli: participantes y enlaces de grupos por el daemon

Preparado, probado e instalado; **NO desplegado**. No se abrieron stores de
producción, no se reiniciaron procesos y no se hicieron operaciones reales
contra WhatsApp. Todas las pruebas usaron stores temporales en `/tmp/opencode`.

**Swap en HOLD, sin fecha programada.** Requiere nueva revisión y aprobación de
G, en una noche distinta del movimiento de pm2. Copiar el artefacto a su ruta
inmutable no lo despliega; arrancar un daemon con él sí cambia su comportamiento,
incluso con todas las gates apagadas (ver «Instalar dark no es inerte»).

Este build **reemplaza al de respuestas rápidas y a los candidatos `fbb1447` y
`2e2921a` revisados por G**. Incluye la base `8f293bd`, la delegación de grupos y
las correcciones de ambas revisiones descritas abajo. Los artefactos anteriores
quedan como evidencia, no como candidatos de despliegue.

## Artefactos

| Qué | Valor |
| --- | --- |
| Rama | `nicolasbiondic/wacli:vendas/fotos-acks-qr-groups` |
| Worktree | `/home/xubuntu/code/new-pedrito/wacli-fork/fotos-acks-qr-groups` |
| Base | `8f293bd02d51c25627e1aa80f475850b29b5407a` (`vendas/fotos-acks-quick-replies`) |
| Commit de código del binario | `13b8cb16e680c6a97996c72bbb022aff6c5cf19a` |
| Binario nuevo (0555) | `/home/xubuntu/.local/share/wacli-diag/0.24.0-vendas-fotos-acks-qr-groups-6d366fc69be1/wacli` |
| `--version` | `wacli 0.24.0+vendas.13b8cb1.502.496.lid.settle.fotos.acks.qr.groups` |
| SHA256 de ambos builds independientes | `6d366fc69be109a654a8dd874f7dc2d74bc9ed8db1aaa14c2291301943d8b941` |
| Sustituye al artefacto QR | `/home/xubuntu/.local/share/wacli-diag/0.24.0-vendas-fotos-acks-qr-80c744ac81b9/wacli` |
| SDK público | `github.com/nicolasbiondic/whatsmeow v0.0.0-20261027060249-339cd8f8aa7d`, rama `vendas/024-snapshot-boundary`, commit `339cd8f8aa7d4257849585acb84d5ec53daf061e` |
| Checksum SDK | `h1:iPNpZ9tU3R5mtxjFUoikeUB+8Jt2V4ai3hRgx9fGhWc=` |
| Otras dependencias | Sin cambios de versión. El SDK sí cambia respecto de `aa93c652`: dos archivos de producción para procedencia/inicio de snapshot y un test nuevo. Los 275 archivos publicados coinciden con `339cd8f` |

El sufijo de 12 caracteres del directorio es el prefijo del SHA256 del
**binario**, no del commit. `go version -m` registra `vcs.modified=false`.

## Qué agrega

- `groups participants add|remove|promote|demote --jid ... --user ...`
- `groups invite link get --jid ...`
- `groups invite link revoke --jid ...` (es el reset existente; no hay un
  subcomando separado `reset`). `groups invite link` solo muestra ayuda.

Si el store está bloqueado por `sync --follow`, los comandos usan el IPC
existente (`.send.sock` en Linux; transporte de `internal/delegate`), versión 1,
con kinds nuevos `group_participants` y `group_invite_link`. Van fuera de la
cola/pacing de envíos, como archive/pin/mute. Reutilizan `--timeout`, deadline
absoluto, margen de respuesta y errores de incertidumbre después del despacho.
No hay reintentos automáticos de estas operaciones.

Sin LOCK conservan el camino autónomo: pueden abrir una conexión propia. Un
LOCK sin socket conserva el error de bloqueo. Un daemon anterior rechaza el
kind nuevo con `unsupported send kind "group_participants"` o
`unsupported send kind "group_invite_link"`; el CLI agrega
``restart `wacli sync` after upgrading``. No hay fallback a otra conexión tras
ese rechazo ni tras un timeout del IPC.

El JSON público no cambia: participantes devuelve el array de whatsmeow,
incluidos `Error` y `AddRequest` por participante, incluso con éxitos parciales.
**`success: true` no significa que todos los participantes cambiaron**: el
gateway debe revisar cada `data[i].Error`. Se conservan `null` y `[]` si la API
los devuelve. El snapshot local se refresca best-effort como antes. Invitación
devuelve `{jid, link}`, más `revoked: true` al resetear.

`invite link get` ahora rechaza `--read-only` explícitamente antes de abrir o
delegar: la operación usa acceso de escritura al store de dispositivo. Es una
guardia necesaria para que el LOCK no permita saltar el modo de solo lectura.

Fotos y acks no cambian. Los demás comandos vivos de grupos siguen sin
delegación. Este build mantiene las migraciones **36 y 38** de listas/respuestas
rápidas y agrega por el schema guard la columna aditiva
`list_sync_state.full_sync_complete`, inicialmente 0. No asume que el flag
histórico `full_sync_events_seen` de un build anterior pruebe completitud.

### Correcciones de la revisión de G

- Solo el sentinel final de `regular`, en el escritor ordenado y después de
  persistir sus mutaciones, puede marcar `complete`. Un evento `FromFullSync`
  solo registra observación; un snapshot parcial o con escritura fallida queda
  `incomplete`. El full sync de arranque captura el snapshot y el sentinel antes
  de persistirlos. También se prueban el sentinel encolado y el retry posterior
  a una falla. Snapshots vacíos verificados sí pueden quedar completos.
- **Segunda revisión:** el SDK ahora preserva `FromFullSync` en los eventos
  genéricos y emite `AppStateSyncStart` antes de las mutaciones de cada snapshot
  full/recovery. Ambos handlers vivos persisten la invalidación al inicio; solo
  el cierre ordenado y exitoso devuelve `complete`. Si falla la invalidación,
  no se guardan las mutaciones. Se mantiene también la invalidación previa a
  cada quick reply full, para no depender de que una etiqueta aparezca primero.
  Regresión con el colector real: estado completo previo → primera reply de
  otro snapshot → salida abrupta sin cerrar la DB → reapertura `incomplete`.
  El flujo sin corte termina `complete`. Antes del fix falló 3/3 en follow y
  one-shot; después pasa en ambos, con inicio full, recovery y solo colector.
  Si se solapan batches full/recovery, no se marca completo mientras quede
  alguno abierto ni se borran errores de un snapshot al comenzar otro. Los
  captures de recovery reproducen límites equilibrados; hay tests de ambos casos.
- Participantes e invitaciones del mismo grupo se serializan hasta terminar el
  refresco del snapshot; otros grupos siguen independientes. La espera consume
  el mismo presupuesto del IPC. EOF y otras respuestas perdidas después del
  despacho ahora indican resultado incierto, igual que un timeout; no implican
  que WhatsApp rechazó la acción ni autorizan un reintento automático.
  La clave del slot se obtiene al parsear el JID y quitar el dispositivo:
  `123@g.us` y `123:0@g.us` comparten exclusión. Su regresión también falla
  antes del fix y pasa después.
- **`WACLI_REGULAR_WRITES` queda sin definir o en 0 por defecto** tanto en el
  gateway/CLI como en cada daemon. Solo `=1` en ambos permite escrituras de
  etiquetas/listas/respuestas rápidas y resync manual. Son operaciones que
  comparten el coordinador de archive/pin y pueden demorarlos. Mantén también
  `QUICK_REPLIES_WRITE_TENANTS` vacío en el gateway. Leer respuestas y el sync
  normal no requieren opt-in. Si G aprueba un resync excepcional, debe habilitar
  expresamente el flag en ambos lados y deshabilitarlo al terminar; los comandos
  de resync del runbook anterior no eluden esta guardia.

## Instalar dark no es inerte

Las gates controlan rutas y mutaciones solicitadas, **no** el esquema ni todo
el trabajo del daemon. Incluso con `WACLI_REGULAR_WRITES` apagado y
`GROUP_PARTICIPANTS_TENANTS` / `QUICK_REPLIES_WRITE_TENANTS` vacíos, arrancar una
línea con este candidato, respecto del build de producción `bee384f`:

- Aplica migraciones 36/38 y el guard de `full_sync_complete` al abrir con escritura.
- Persiste labels, membresías y quick replies en segundo plano; una membresía
  puede crear un chat local y genera bookkeeping de recovery.
- Incluye `chat_lists` en el descubrimiento/merge de identidades LID/PN, también
  en aperturas autenticadas de comandos autónomos.
- No agrega un fetch inicial incondicional: producción ya sincronizaba
  `regular` por completo. Pero la deuda de persistencia de `regular` puede
  provocar replays o recuperación adicional desde el teléfono.
- Hace más trabajo en el escritor SQLite compartido con archive/pin, con riesgo
  de contención/timing aunque sus algoritmos no cambien.
- Atiende comandos locales de grupos por IPC independientemente de la allowlist
  HTTP del gateway; las gates del gateway no bloquean esos callers locales.

Por eso no se instala en las líneas «para dejarlo listo» sin una decisión de
despliegue. No hay swap programado; la aprobación y las comprobaciones de la
nueva ventana siguen pendientes. Esta tarea no verificó producción.

## Dos ventanas distintas: pm2 primero, wacli otra noche

**No mezcles los cambios.** G programó `~/vendas-ops/pm2-pin.sh` para las **02:30
Lima del 27-oct-2026**: pin a pm2 **7.1.1**, movimiento/reinicio del daemon pm2 y
resurrección de su lista guardada. Este implementador solo leyó el script; no lo
ejecutó ni verificó el estado de producción.

El cambio de wacli va **OTRA noche**, nunca en esa misma ventana ni como segunda
parte automática del script. G debe confirmar que terminó el movimiento y
aprobar la nueva ventana. Un preflight o snapshot anterior al movimiento de pm2
no sirve como baseline de la ventana de wacli.

En la nueva noche, antes de tocar una línea, exige un **preflight nuevo**:

1. El PID de `~/.pm2/pm2.pid` está vivo y su proceso declara daemon **7.1.1**;
   el cliente pm2 es también 7.1.1. Comprueba primero el daemon por PID, sin
   lanzar un cliente que pueda crear otro daemon si no existe.
2. `PM2_BIN` del gateway y del control apunta al ejecutable absoluto versionado
   que eligió `pm2-pin.sh` dentro de `npm-pm2/7.1.1`, no a `latest`, `7` ni a un
   shim de mise. La ruta puede ser `node_modules/.bin/pm2`, `bin/pm2` o
   `lib/node_modules/pm2/bin/pm2`: valida la real, no supongas el layout.
3. Confirma qué `PM2_HOME`, unidad systemd y PID poseen la lista. Si G usó
   `--outside-unit`, registra la excepción autorizada y el plan de arranque al
   próximo boot; no lo confundas con una unidad activa. Comprueba que
   `~/.pm2/dump.pm2` es el estado guardado por el daemon nuevo y corresponde a
   la lista actual. No resucites un dump anterior ni imprimas secretos del dump.
4. Comprueba `/health` y el estado **authenticated/online de las cuatro líneas**
   mediante el gateway: Nexus, 374, 520 y 964. Registra PID, ejecutable real,
   contador de reinicios, config activa, base, heartbeat y socket/LOCK por línea.
   Observa que los reinicios ya no crecen y que no aparecen revocaciones/401 ni
   errores SQLite. No uses una operación de grupo como prueba.
5. Si pm2 y cliente no coinciden, falta una línea, el estado guardado es dudoso o
   el movimiento no terminó sano, **detente y aplaza wacli**. No ejecutes
   `pm2-pin.sh` otra vez como parte de este despliegue.

## Despliegue: mismo procedimiento por línea

Usa `/home/xubuntu/code/new-pedrito/RUNBOOK-wacli-quick-replies.md`, secciones
«Variables», §1–§7 y «Rollback», junto con el runbook de fotos que referencia.
**Cambia `NEW` y su checksum por los de arriba y aplica las guardias nuevas de
esta revisión.** `OLD` debe ser el
artefacto que realmente tenga esa línea en el momento de desplegar, no el
artefacto QR por suposición: aquel build también quedó preparado sin desplegar.

1. Preflight nuevo posterior a pm2 y backup de configs del procedimiento existente.
2. **Antes de que el binario nuevo abra el store**, para solo esa línea y toma
   el LOCK exclusivo (aborta si no se libera). Con la línea cerrada y el LOCK
   retenido, crea un snapshot SQLite de **`session.db` y `wacli.db` como pareja**
   mediante `sqlite3.Connection.backup`, como en el runbook de fotos. Verifica
   `PRAGMA integrity_check` de ambas copias. Respalda también config activa,
   config base, ruta/versión/checksum de `OLD` y los archivos locales necesarios
   (por ejemplo, medios); guarda todo en un directorio privado 0700, bases 0600.
   Anota la hora/corte del snapshot. No copies WAL vivos a mano ni saltes el
   snapshot porque las migraciones son aditivas.
3. Actualiza **una línea por vez** con el mismo procedimiento de LOCK y snapshot;
   verifica proceso, ejecutable real, heartbeat y logs antes de seguir.
4. Actualiza también la configuración base de esa línea. No basta cambiar
   `WACLI_BIN`: el daemon debe arrancar con el nuevo ejecutable.
5. Sigue el orden daemons primero, gateway/`WACLI_BIN` después. Mantén apagadas
   ambas gates de escritura y verifica lectura de respuestas rápidas: durante
   un snapshot parcial debe verse `incomplete`, no se fuerza `complete` a mano.
6. Guarda el estado de pm2 nuevo solo después de validar la línea. Si falla,
   no continúes a la siguiente: usa el rollback de abajo.

No pruebes participantes ni revoke/reset en producción para verificar el
despliegue. Tampoco uses `get` como sondeo de capacidades: haría una consulta
real. Un timeout de mutación o revoke es incierto; comprueba el estado antes de
reintentar. Nunca lances estos comandos a mano con el LOCK libre de una línea.

## Rollback de una línea y restauración del snapshot

Hay una prueba offline del binario anterior exacto
`/home/xubuntu/.local/share/wacli-diag/0.24.0-vendas-fotos-acks-b37c8f109faa/wacli`
sobre un store migrado por el ejecutable nuevo: abre con y sin `--read-only`,
lee chats/mensajes/cambios/grupos/contactos/auth, hace un purge **local** y pasa
doctor. Las tablas nuevas y su contenido sobreviven. No equivale a probar una
conexión autenticada: no hay modo offline de sync; `sync --once` abre ambas bases
en una sesión temporal sin autenticar y se detiene antes de `Connect`. Nunca se
usó una sesión real para esta prueba.

Por esa compatibilidad se puede elegir rollback **solo de binario**, preservando
datos posteriores al snapshot, si las bases siguen sanas. No lo presentes como
única salida: registra explícitamente la decisión y conserva las copias.

**Procedimiento de rollback con restauración**, si hay una falla de store o G
ordena volver al snapshot:

1. Suspende operaciones de esa línea en el gateway y para solo su daemon con
   el pm2 **7.1.1 validado**, sin reiniciar el daemon global de pm2. Conserva
   primero una copia de diagnóstico del store que falla y de sus configs.
2. Adquiere y retén el LOCK exclusivo; si no puedes, aborta. No ejecutes un CLI
   de escritura en paralelo ni reemplaces/elimines el archivo LOCK mientras
   está retenido (cambiar su inode rompería la exclusión).
3. Comprueba fecha/corte e integridad del snapshot previo al cambio. Restaura
   **las dos bases de la misma pareja** (`session.db` + `wacli.db`), permisos
   0600; retira únicamente los sidecars WAL/SHM del estado sustituido, con todos
   los escritores cerrados. No combines una base actual con otra antigua ni
   restaures sockets/heartbeats como si fueran datos. Restaura archivos locales
   que esas bases referencien, si se respaldaron.
4. Restaura las configs activa y base respaldadas, apuntando al `OLD` verificado;
   libera el LOCK antes de arrancar una sola vez por el pm2 validado. Verifica
   `/proc/PID/exe`, estado/autenticación, logs y heartbeat antes de devolver
   tráfico. Solo entonces guarda el estado de pm2; no vuelvas a un dump global
   previo al movimiento de pm2.
5. Restaurar retrocede los mensajes y el estado criptográfico de la sesión al
   corte. **No garantiza recuperar datos ni reconectar sin incidentes**: puede
   perder datos posteriores, exigir replay/resincronización y afectar ratchets.
   G debe aceptar ese riesgo y reconciliar con el gateway antes de seguir. Si
   aparece revocación o sesión incoherente, detente; no repares una sesión real
   por ensayo y error. No hay prueba real de esa reconexión en esta tarea.

La prueba reproducible también ensaya restaurar ambas bases del snapshot
temporal y volver a abrirlas con el viejo; confirma que desaparecen 36 y 38.

## Detección de capacidad para el gateway

La capacidad es **por tenant y por ejecutable dueño**, no solo por `WACLI_BIN`.

- El CLI soporta este parche si su `--version` coincide con la versión de la
  tabla (o una versión posterior explícitamente aprobada). No basta `0.24.0`.
- Como alternativa sin store/red, cada uno de los seis subcomandos muestra en
  `--help` la frase exacta:
  `Delegates to the running sync process when the store is locked.`
- Para anunciar la capacidad de un tenant, exige también que su daemon esté
  online, tenga el LOCK y el socket disponible, y que **el ejecutable que corre
  ese PID** soporte el parche. En Linux puedes verificar la versión o el help
  ejecutando `/proc/<pid-del-daemon>/exe --version` o
  `/proc/<pid-del-daemon>/exe groups participants add --help`, sin pasar store.
  No infieras la capacidad desde argv, un symlink actualizado o el `.env`: un
  proceso anterior puede seguir corriendo aunque `WACLI_BIN` ya sea nuevo.
- Si no puedes verificar el dueño, no anuncies la capacidad. No hay un endpoint
  IPC nuevo de capabilities en este diff. Conserva el rechazo de kind viejo como
  respaldo y mapea el error a `503 daemon_unsupported` con pista de actualización,
  igual que respuestas rápidas; no reintentes por una conexión paralela.

Cambiar el binario solo no habilita una capacidad existente del gateway: G debe
incorporar esa detección al contrato/anuncio correspondiente.

## Verificaciones

Con `TMPDIR=/tmp/opencode`, `CGO_ENABLED=1` y
`CGO_CFLAGS=-Wno-error=missing-braces`:

- `go test -count=1 -tags sqlite_fts5 ./...`: OK, todos los paquetes.
- `go test -count=1 ./...`: OK, todos los paquetes.
- `go vet ./...` y `go vet -tags sqlite_fts5 ./...`: OK.
- Tests de grupos con `-race -tags sqlite_fts5`: OK.
- Race completo de `cmd/wacli`, `internal/app`, `internal/store` e `internal/wa`: OK.
- `scripts/check-deadcode.sh`: OK, producción y tests.
- Cross-compile Windows de lock/store y guardia CGO obligatorio: OK.
- Tests Node de docs/release: 32 OK; build del sitio de docs: OK.
- `gofmt`, `git diff --check`, build FTS y ayuda/version del binario instalado: OK.
- Regresiones de crash y alias de grupo: fallo confirmado antes del fix, tres
  repeticiones; pasan después. El crash usa el colector SDK real y `os.Exit`,
  sin cierre de App/SQLite antes de la reapertura.
- SDK: tests nuevos con race y vet del paquete raíz: OK, solo fixtures sintéticas.
- Dos builds con `-trimpath`, clones distintos y cachés de módulos/compilación
  vacías e independientes: SHA256 idéntico y `cmp` byte a byte OK.
- Smoke del binario instalado con dueño IPC simulado y LOCK temporal: 14 casos
  OK (las seis operaciones, dueño viejo, arrays `null`/vacíos). La ayuda del
  binario QR anterior no contiene el marcador de capacidad.

Logs de esta revisión: `/tmp/opencode/wacli-groups-round2-{before-fix,after-fix,test-fts,test-plain,vet,vet-fts,race,deadcode,windows,doc-tests,doc-site,cgo-required,rollback,sdk-tests,sdk-vet}.log`.
Información del artefacto: `/tmp/opencode/wacli-groups-round2-build-info.txt`.
Prueba SDK: `/tmp/opencode/wacli-groups-round2-sdk-proof.txt`.
Smoke instalado: `/tmp/opencode/wacli-groups-round2-installed-smoke.log` (script
`/tmp/opencode/wacli-groups-installed-smoke.py`).

## Reproducir el artefacto sin desplegar

El artefacto se construyó **dos veces**, con clones HTTPS limpios, rutas distintas
y cachés `GOMODCACHE` y `GOCACHE` inicialmente vacías e independientes. Se usó
Go 1.27.3, Linux amd64 y el mismo compilador C en ambos; `-trimpath` quita las
rutas de construcción. Evidencia en `/tmp/opencode/wacli-groups-round2-repro-final/`:
`a/` y `b/` contienen clone/build/verify logs, build-info y ejecutable;
`sha256.log` registra el digest idéntico de ambos y `toolchain.txt` el toolchain.
`cmp a/wacli b/wacli` pasó. No se requiere un directorio hermano whatsmeow.

El SDK exacto está publicado en
`https://github.com/nicolasbiondic/whatsmeow/tree/vendas/024-snapshot-boundary`.
El commit anterior `aa93c652` sigue disponible en `vendas/024` para reproducir
builds viejos; **no sirve para reconstruir este candidato** sin el parche nuevo.

Este es el comando del build (mismos flags en ambos clones; `-p 4` limita la
carga del compilador). Con el mismo Go 1.27.3 y compilador C, usa destinos nuevos
bajo `/tmp/opencode`:

```bash
set -euo pipefail
ROOT=/tmp/opencode/wacli-groups-rebuild-round2
test ! -e "$ROOT"
mkdir -m0700 "$ROOT"
for side in a b; do
  mkdir "$ROOT/$side"
  git clone --single-branch --branch vendas/fotos-acks-qr-groups \
    https://github.com/nicolasbiondic/wacli.git "$ROOT/$side/src"
  git -C "$ROOT/$side/src" checkout --detach 13b8cb16e680c6a97996c72bbb022aff6c5cf19a
  test -z "$(git -C "$ROOT/$side/src" status --porcelain)"
  bash /tmp/opencode/wait-load.sh 1400
  TMPDIR=/tmp/opencode GOWORK=off GOFLAGS= \
  GOMODCACHE="$ROOT/$side/modules" GOCACHE="$ROOT/$side/build-cache" \
  GOPROXY=https://proxy.golang.org,direct \
  CGO_ENABLED=1 CGO_CFLAGS=-Wno-error=missing-braces \
    go -C "$ROOT/$side/src" build -p 4 -trimpath -tags sqlite_fts5 \
      -ldflags '-X main.version=0.24.0+vendas.13b8cb1.502.496.lid.settle.fotos.acks.qr.groups' \
      -o "$ROOT/$side/wacli" ./cmd/wacli
  GOMODCACHE="$ROOT/$side/modules" GOWORK=off go -C "$ROOT/$side/src" mod verify
  go version -m "$ROOT/$side/wacli"
done
sha256sum "$ROOT/a/wacli" "$ROOT/b/wacli"
cmp "$ROOT/a/wacli" "$ROOT/b/wacli"
```

El `=>` de `go version -m` debe mostrar la pseudoversión y `h1:` de la tabla,
además de `-trimpath=true` y `vcs.modified=false`. Los **275 archivos** del módulo
descargado se compararon byte a byte con el commit público `339cd8f`; no falta
ni sobra ninguno. Su diferencia intencional respecto de `aa93c652` son
`appstate.go`, `types/events/appstate.go` y el test
`appstate_snapshot_boundary_test.go`, no una actualización general de dependencias.
Los dos builds dieron exactamente el SHA256 de la tabla. Esta prueba demuestra
reproducibilidad byte a byte entre rutas/cachés distintas con el mismo toolchain;
no promete el mismo binario con versiones diferentes de Go o del compilador C.

Para repetir el rollback offline con los binarios reales (solo crea stores
temporales; la fixture de ejecutables FTS se ejecuta con `sqlite_fts5`):

```bash
bash /tmp/opencode/wait-load.sh 1400
TMPDIR=/tmp/opencode CGO_ENABLED=1 CGO_CFLAGS=-Wno-error=missing-braces \
WACLI_COMPAT_BIN=/home/xubuntu/.local/share/wacli-diag/0.24.0-vendas-fotos-acks-b37c8f109faa/wacli \
WACLI_NEW_BIN=/home/xubuntu/.local/share/wacli-diag/0.24.0-vendas-fotos-acks-qr-groups-6d366fc69be1/wacli \
  go test -count=1 -v -tags sqlite_fts5 ./internal/store \
  -run 'TestQuickReplies.*(Previous|Rollback)'
```

Instala con `0555` en un directorio nuevo identificado por el prefijo SHA256.
Nunca sobrescribas un artefacto inmutable ni cambies el binario de una línea
sin seguir el procedimiento completo anterior.
