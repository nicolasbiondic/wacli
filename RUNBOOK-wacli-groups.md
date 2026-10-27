# wacli: participantes y enlaces de grupos por el daemon

Preparado, probado e instalado; **NO desplegado**. No se abrieron stores de
producción, no se reiniciaron procesos y no se hicieron operaciones reales
contra WhatsApp. Todas las pruebas usaron stores temporales en `/tmp/opencode`.

**Swap en HOLD, sin fecha programada.** Requiere nueva revisión y aprobación de
G, en una noche distinta del movimiento de pm2. Copiar el artefacto a su ruta
inmutable no lo despliega; arrancar un daemon con él sí cambia su comportamiento,
incluso con todas las gates apagadas (ver «Instalar dark no es inerte»).

Este build **reemplaza al de respuestas rápidas y a los candidatos `fbb1447`,
`2e2921a`, `13b8cb1` y `04bd77c` revisados por G**. Incluye la base `8f293bd`,
la delegación de grupos y las correcciones de las cuatro revisiones descritas
abajo. Los artefactos anteriores quedan como evidencia, no como candidatos de
despliegue.

## Artefactos

| Qué | Valor |
| --- | --- |
| Rama | `nicolasbiondic/wacli:vendas/fotos-acks-qr-groups` |
| Worktree | `/home/xubuntu/code/new-pedrito/wacli-fork/fotos-acks-qr-groups` |
| Base | `8f293bd02d51c25627e1aa80f475850b29b5407a` (`vendas/fotos-acks-quick-replies`) |
| Commit de código del binario | `e0f3fb40033ffd7ec10f12c7c26d736bd9af2ef2` |
| Binario nuevo (0555) | `/home/xubuntu/.local/share/wacli-diag/0.24.0-vendas-fotos-acks-qr-groups-c4dbf28208c0/wacli` |
| `--version` | `wacli 0.24.0+vendas.e0f3fb4.502.496.lid.settle.fotos.acks.qr.groups` |
| SHA256 de ambos builds independientes | `c4dbf28208c0c5fec02bdbe6ce37dd4673261adba8f44fa8770f33135ba8230f` |
| Sustituye al artefacto QR | `/home/xubuntu/.local/share/wacli-diag/0.24.0-vendas-fotos-acks-qr-80c744ac81b9/wacli` |
| SDK público | `github.com/nicolasbiondic/whatsmeow v0.0.0-20261027081841-579078a41ce7`, rama `vendas/024`, commit `579078a41ce7e7d2b66ef7c4a7c241e6fbb020b6` |
| Checksum SDK | `h1:tjIdk4hWmWOb3o3hflg9qahBFMV/lsBCGlb6Fy/k9Lc=` |
| Otras dependencias | Sin cambios de versión respecto de `04bd77c`, incluido el SDK `579078a`. Los 275 archivos del módulo de la reconstrucción final coinciden con ese commit público |

El sufijo de 12 caracteres del directorio es el prefijo del SHA256 del
**binario**, no del commit. `go version -m` registra `vcs.modified=false`.
El candidato intermedio `379173c` (`…-beb608969d71/wacli`) queda descartado:
su retry de apertura no cubría el mismo error durante el SELECT posterior.
No se desplegó ni se sobrescribió. Solo el artefacto de la tabla es el candidato.

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
- **Tercera revisión, lector:** `QuickReplies()` y `Lists()` leen flags,
  metadatos de esquema y filas dentro de una única transacción de lectura
  SQLite (`ReadView`, `BEGIN DEFERRED`). El snapshot WAL no puede mezclar el
  `complete` anterior con filas parciales nuevas. También comparten vista las
  lecturas para elegir ID de label y los contadores del resync. Se conserva el
  contrato `unknown` de listas: no se promueve el flag informativo a completitud.
  La regresión pausa el SELECT real del flag con una barrera de test, mientras
  otra conexión WAL invalida y persiste en bucle: falló 3/3 antes del fix y pasó
  **20/20 con race**, tanto plain como FTS. No hay sleeps ni hooks de producción.
  El artefacto instalado pasó además 300 lecturas con escritor concurrente en
  tres stores desechables, sin `complete` con filas parciales.
- **Tercera revisión, snapshots de conflicto:** el SDK acepta snapshots con
  estado vacío por el camino full, con start, procedencia full y cierre. Los
  patches que siguen al snapshot conservan semántica incremental. Un MAC
  inválido descarta el batch completo. Tests con dos quick replies cifradas y
  MAC verificado, snapshot corrupto y delta posterior: OK.
- **Tercera revisión, recovery rechazado:** el SDK mantiene estable el conjunto
  de handlers durante el batch y, en defer, entrega `AppStateSyncComplete` con
  `Failed=true` solo a quienes recibieron inicio sin aceptar un cierre exitoso.
  También cubre panic; un rechazo del cleanup no impide cerrar los demás.
  **No es un sentinel exitoso.** Wacli invalida, balancea el contador y conserva
  `incomplete`; un retry exitoso completa en la misma vida del App. Los cierres
  fallidos tampoco saldan recovery de archive. Tests de rechazo al inicio,
  mutación y cierre; retry en follow, one-shot y capture: OK.
- Participantes e invitaciones del mismo grupo se serializan hasta terminar el
  refresco del snapshot; otros grupos siguen independientes. La espera consume
  el mismo presupuesto del IPC. EOF y otras respuestas perdidas después del
  despacho ahora indican resultado incierto, igual que un timeout; no implican
  que WhatsApp rechazó la acción ni autorizan un reintento automático.
  La clave del slot se obtiene al parsear el JID y quitar el dispositivo:
  `123@g.us` y `123:0@g.us` comparten exclusión. Su regresión también falla
  antes del fix y pasa después.
  En la tercera revisión se valida además el destino antes del slot y de la
  ejecución: usuario numérico no vacío, servidor `g.us`, un único `@` y aliases
  AD bien formados en rango (device uint16, agent uint8). Se aceptan IDs modernos
  y legacy `número-número`. Teléfonos, LIDs y strings mal formados no llegan al
  cliente; mutación, refresco y resultado de invitación usan el mismo JID sin
  dispositivo. La CLI valida antes de abrir un store autónomo.
- **Cuarta revisión, resolución de listas:** `ResolveList()` comparte
  `ReadView` entre definiciones activas y tombstones. `chats list --list` extiende
  esa misma vista hasta cargar membresías; todas esas consultas usan
  `readQuery()`. Se mantiene la precedencia del ID exacto: si el 7 está borrado
  y el 8 se llama «7», un undelete concurrente no puede devolver chats del 8.
  La regresión determinista de tombstones y membresías falló 3/3 por modo antes
  del fix; pasó **20/20 con race por modo plain/FTS** después. La sonda del
  ejecutable final hizo **3.000 intentos CLI** en tres stores WAL con escritor
  concurrente: 1.408 respuestas con el chat correcto, 1.592 errores esperados
  de ID borrado, cero listas equivocadas y cero errores inesperados. No hay
  retries externos en la sonda.
  Se revisaron los otros lectores: `Lists()`/`ResolveListForWrite()` trabajan
  sobre una sola vista de definiciones; `ListByID()` y `MaxNumericListID()` usan
  `readQuery()`, y creación de labels/resync ya comparten `ReadView`. No se
  modificaron el escritor de app-state ni el SDK.
- **Cuarta revisión, condición read-only:** se reintenta exclusivamente el
  código extendido `SQLITE_READONLY_RECOVERY` (264), tanto en el SELECT de
  validación de apertura como en `ReadView` de una conexión `OpenReadOnly`.
  Son cuatro intentos, con pausas de 5/10/15 ms; cada retry de vista hace
  rollback y reinicia la transacción completa, sin mezclar snapshots. La
  primera sonda confirmó que el error también aparece después de abrir
  (diagnóstico tipado en un worktree temporal); por eso no basta el retry de
  apertura. Tests de inyección cubren éxito, agotamiento, error envuelto,
  otros errores, vistas anidadas, nueva transacción y exclusión de conexiones
  RW/mutaciones. No cambia la política SHM ni añade retries a migraciones o
  escrituras. El agotamiento sigue siendo un error; una sonda de 3.000 intentos
  sin errores inesperados no garantiza ausencia futura de errores. Los helpers
  CGO/no-CGO conservan el cross-compile de tests y la guardia CGO explícita del CLI.
- **`WACLI_REGULAR_WRITES` queda sin definir o en 0 por defecto** tanto en el
  gateway/CLI como en cada daemon. Solo `=1` en ambos permite escrituras de
  etiquetas/listas/respuestas rápidas y resync manual. Son operaciones que
  comparten el coordinador de archive/pin y pueden demorarlos. Mantén también
  `QUICK_REPLIES_WRITE_TENANTS` vacío en el gateway. Leer respuestas y el sync
  normal no requieren opt-in. Si G aprueba un resync excepcional, debe habilitar
  expresamente el flag en ambos lados y deshabilitarlo al terminar; los comandos
  de resync del runbook anterior no eluden esta guardia.

### Condiciones antes de encender escrituras

Los tres puntos previos a escrituras de la tercera revisión y la condición
read-only de la cuarta están implementados; no se difirió ninguno. Eso **no
autoriza** habilitarlo: exige la re-revisión de G de esta corrección y las
regresiones, su go explícito, una noche distinta de pm2 y el preflight nuevo de
abajo. El SDK permanece en el commit público ya revisado. Mantén
`WACLI_REGULAR_WRITES` apagado y las allowlists del gateway vacías hasta esa
decisión. Los tests son offline con fixtures sintéticas; no prueban aceptación/
permisos de WhatsApp ni reconexión autenticada tras rollback.

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
- Race completo de `cmd/wacli`, `internal/app`, `internal/store` e `internal/wa`: OK en plain y FTS.
- `scripts/check-deadcode.sh`: OK, producción y tests.
- Cross-compile Windows de lock/store y guardia CGO obligatorio: OK.
- Tests Node de docs/release: 32 OK; build del sitio de docs: OK.
- `gofmt`, `git diff --check`, build FTS y ayuda/version del binario instalado: OK.
- Regresiones de crash y alias de grupo: fallo confirmado antes del fix, tres
  repeticiones; pasan después. El crash usa el colector SDK real y `os.Exit`,
  sin cierre de App/SQLite antes de la reapertura.
- Lector WAL: `go test -count=20 -race [-tags sqlite_fts5] ./internal/store -run '^TestCompletenessAndRowsShareOneWALSnapshot$'`: 20/20 en cada modo.
- Resolución/tombstones/membresías: `TestListResolutionAndMembershipsShareOneWALSnapshot`, 20/20 con race en cada modo; fallo confirmado 3/3 por modo antes del fix.
- Retry read-only: tests de error inyectado `TestReadOnlyOpenRetriesOnlyReadonlyRecovery` y `TestReadOnlyRecoveryRestartsWholeReadViewOnly`, 20/20 con race por modo.
- SDK sin cambios: tests completos/race/vet de raíz, `appstate` y `types/events`
  ya pasaron en la tercera revisión; esta ronda vuelve a verificar los 275/275
  archivos del módulo y su checksum desde una caché vacía.
- Cierres fallidos y retry, follow/one-shot/capture: OK, sin aperturas filtradas.
- Artefacto instalado: 3.000 intentos reales `chats list --list 7` con writer
  concurrente en tres stores WAL desechables; sin chats de la lista equivocada
  ni errores inesperados. El writer confirmó 432.837 toggles en total.
- Dos builds con `-trimpath`, clones distintos y cachés de módulos/compilación
  vacías e independientes: SHA256 idéntico y `cmp` byte a byte OK.
- Smoke del binario instalado con dueño IPC simulado y LOCK temporal: 14 casos
  OK (las seis operaciones, dueño viejo, arrays `null`/vacíos). La ayuda del
  binario QR anterior no contiene el marcador de capacidad.

Logs de esta revisión: `/tmp/opencode/wacli-groups-round4-{before-plain,before-fts,retry-before,reader-plain,reader-fts,cli-provisional,artifact-list,test-fts,test-plain,vet,vet-fts,race-plain,race-fts,deadcode,windows,doc-tests,doc-site,cgo-required,rollback}.log`.
Diagnóstico del error posterior a apertura: `/tmp/opencode/wacli-groups-round4-readonly-diagnostic.log`.
Información del artefacto: `/tmp/opencode/wacli-groups-round4-build-info.txt`.
Prueba SDK: `/tmp/opencode/wacli-groups-round4-sdk-proof.txt`.
Diff de código: `/tmp/opencode/wacli-groups-round4-code.diff`.
El script `/tmp/opencode/wacli-groups-round4-artifact-list.py` verifica la CLI
instalada; solo crea stores temporales sin autenticar. Por defecto ejecuta
1.000 intentos en cada uno de tres stores, sin retries externos.
Smoke instalado: `/tmp/opencode/wacli-groups-round4-installed-smoke.log` (script
`/tmp/opencode/wacli-groups-installed-smoke.py`).

## Reproducir el artefacto sin desplegar

El artefacto se construyó **dos veces**, con clones HTTPS limpios, rutas distintas
y cachés `GOMODCACHE` y `GOCACHE` inicialmente vacías e independientes. Se usó
Go 1.27.3, Linux amd64 y el mismo compilador C en ambos; `-trimpath` quita las
rutas de construcción. Evidencia en `/tmp/opencode/wacli-groups-round4-repro-final/`:
`a/` y `b/` contienen clone/build/verify logs, build-info y ejecutable;
`sha256.log` registra el digest idéntico de ambos y `toolchain.txt` el toolchain.
`cmp a/wacli b/wacli` pasó. No se requiere un directorio hermano whatsmeow.

El SDK exacto está publicado en
`https://github.com/nicolasbiondic/whatsmeow/tree/vendas/024`, en `579078a`.
`339cd8f` permanece en la historia (y en `vendas/024-snapshot-boundary`) para
builds viejos; **no sirve para reconstruir este candidato** sin los nuevos fixes.

Este es el comando del build (mismos flags en ambos clones; `-p 4` limita la
carga del compilador). Con el mismo Go 1.27.3 y compilador C, usa destinos nuevos
bajo `/tmp/opencode`:

```bash
set -euo pipefail
ROOT=/tmp/opencode/wacli-groups-rebuild-round4
test ! -e "$ROOT"
mkdir -m0700 "$ROOT"
for side in a b; do
  mkdir "$ROOT/$side"
  git clone --single-branch --branch vendas/fotos-acks-qr-groups \
    https://github.com/nicolasbiondic/wacli.git "$ROOT/$side/src"
  git -C "$ROOT/$side/src" checkout --detach e0f3fb40033ffd7ec10f12c7c26d736bd9af2ef2
  test -z "$(git -C "$ROOT/$side/src" status --porcelain)"
  bash /tmp/opencode/wait-load.sh 1400
  TMPDIR=/tmp/opencode GOWORK=off GOFLAGS= \
  GOMODCACHE="$ROOT/$side/modules" GOCACHE="$ROOT/$side/build-cache" \
  GOPROXY=https://proxy.golang.org,direct \
  CGO_ENABLED=1 CGO_CFLAGS=-Wno-error=missing-braces \
    go -C "$ROOT/$side/src" build -p 4 -trimpath -tags sqlite_fts5 \
      -ldflags '-X main.version=0.24.0+vendas.e0f3fb4.502.496.lid.settle.fotos.acks.qr.groups' \
      -o "$ROOT/$side/wacli" ./cmd/wacli
  GOMODCACHE="$ROOT/$side/modules" GOWORK=off go -C "$ROOT/$side/src" mod verify
  go version -m "$ROOT/$side/wacli"
done
sha256sum "$ROOT/a/wacli" "$ROOT/b/wacli"
cmp "$ROOT/a/wacli" "$ROOT/b/wacli"
```

El `=>` de `go version -m` debe mostrar la pseudoversión y `h1:` de la tabla,
además de `-trimpath=true` y `vcs.modified=false`. Los **275 archivos** del módulo
descargado se compararon byte a byte con el commit público `579078a`; no falta
ni sobra ninguno. Su diferencia intencional respecto de `339cd8f` son
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
WACLI_NEW_BIN=/home/xubuntu/.local/share/wacli-diag/0.24.0-vendas-fotos-acks-qr-groups-c4dbf28208c0/wacli \
  go test -count=1 -v -tags sqlite_fts5 ./internal/store \
  -run 'TestQuickReplies.*(Previous|Rollback)'
```

Instala con `0555` en un directorio nuevo identificado por el prefijo SHA256.
Nunca sobrescribas un artefacto inmutable ni cambies el binario de una línea
sin seguir el procedimiento completo anterior.
