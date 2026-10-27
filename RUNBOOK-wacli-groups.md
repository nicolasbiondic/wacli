# wacli: participantes y enlaces de grupos por el daemon

Preparado, probado e instalado; **NO desplegado**. No se abrieron stores de
producción, no se reiniciaron procesos y no se hicieron operaciones reales
contra WhatsApp. Todas las pruebas usaron stores temporales en `/tmp/opencode`.

Este build **reemplaza al de respuestas rápidas**, no es una alternativa sin
ellas: incluye exactamente la base `8f293bd` y la delegación de grupos.

## Artefactos

| Qué | Valor |
| --- | --- |
| Rama | `nicolasbiondic/wacli:vendas/fotos-acks-qr-groups` |
| Worktree | `/home/xubuntu/code/new-pedrito/wacli-fork/fotos-acks-qr-groups` |
| Base | `8f293bd02d51c25627e1aa80f475850b29b5407a` (`vendas/fotos-acks-quick-replies`) |
| Commit de código del binario | `fbb1447f58f4c4af8d776280bbc2040f6cdab630` |
| Binario nuevo (0555) | `/home/xubuntu/.local/share/wacli-diag/0.24.0-vendas-fotos-acks-qr-groups-fbcfa64898db/wacli` |
| `--version` | `wacli 0.24.0+vendas.fbb1447.502.496.lid.settle.fotos.acks.qr.groups` |
| SHA256 | `fbcfa64898db2bdd51a7b4db86f1d36e7cf468bd3d106463e83d9045377726c5` |
| Sustituye al artefacto QR | `/home/xubuntu/.local/share/wacli-diag/0.24.0-vendas-fotos-acks-qr-80c744ac81b9/wacli` |
| Dependencias | Sin cambios; lista `dep`/`=>` de `go version -m` idéntica al artefacto QR. whatsmeow sigue en `../whatsmeow-024`, commit `aa93c652` |

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

No cambia fotos, acks, respuestas rápidas, migraciones, dependencias ni flags
de sync. Los demás comandos vivos de grupos siguen sin delegación.

## Despliegue: mismo procedimiento por línea

Usa `/home/xubuntu/code/new-pedrito/RUNBOOK-wacli-quick-replies.md`, secciones
«Variables», §1–§7 y «Rollback», junto con el runbook de fotos que referencia.
**Solo cambia `NEW` y su checksum por los de arriba.** `OLD` debe ser el
artefacto que realmente tenga esa línea en el momento de desplegar, no el
artefacto QR por suposición: aquel build también quedó preparado sin desplegar.

1. Preflight y backup del procedimiento existente.
2. Actualiza **una línea por vez** con el mismo procedimiento de LOCK y snapshot;
   verifica proceso, ejecutable real, heartbeat y logs antes de seguir.
3. Actualiza también la configuración base de esa línea. No basta cambiar
   `WACLI_BIN`: el daemon debe arrancar con el nuevo ejecutable.
4. Sigue el orden daemons primero, gateway/`WACLI_BIN` después, sin alterar las
   gates ni las verificaciones de respuestas rápidas del runbook anterior.
5. Rollback por línea al ejecutable guardado, **sin restaurar bases por este
   cambio**: no agrega migraciones respecto de QR. Si vuelves al build previo
   a QR, aplica además las comprobaciones de compatibilidad de aquel runbook.

No pruebes participantes ni revoke/reset en producción para verificar el
despliegue. Tampoco uses `get` como sondeo de capacidades: haría una consulta
real. Un timeout de mutación o revoke es incierto; comprueba el estado antes de
reintentar. Nunca lances estos comandos a mano con el LOCK libre de una línea.

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
- `scripts/check-deadcode.sh`: OK, producción y tests.
- Cross-compile Windows de lock/store y guardia CGO obligatorio: OK.
- Tests Node de docs/release: 32 OK; build del sitio de docs: OK.
- `gofmt`, `git diff --check`, build FTS y ayuda/version del binario instalado: OK.
- Smoke del binario instalado con dueño IPC simulado y LOCK temporal: 14 casos
  OK (las seis operaciones, dueño viejo, arrays `null`/vacíos). La ayuda del
  binario QR anterior no contiene el marcador de capacidad.

Logs: `/tmp/opencode/wacli-groups-{test-fts,test-plain,vet,vet-fts,race,deadcode,windows,doc-tests,doc-site,cgo-required}.log`.
Información del artefacto: `/tmp/opencode/wacli-groups-build-info.txt`.
Smoke instalado: `/tmp/opencode/wacli-groups-installed-smoke.log` (script
`/tmp/opencode/wacli-groups-installed-smoke.py`).

## Reproducir el artefacto sin desplegar

En un worktree limpio del commit de código de la tabla, con el mismo whatsmeow
y Go 1.27.3, ejecuta primero `bash /tmp/opencode/wait-load.sh 1400` y luego:

```bash
TMPDIR=/tmp/opencode CGO_ENABLED=1 CGO_CFLAGS=-Wno-error=missing-braces \
  go build -tags sqlite_fts5 \
  -ldflags '-X main.version=0.24.0+vendas.fbb1447.502.496.lid.settle.fotos.acks.qr.groups' \
  -o /tmp/opencode/wacli-fotos-acks-qr-groups ./cmd/wacli
```

Instala con `0555` en un directorio nuevo identificado por el prefijo SHA256.
Nunca sobrescribas un artefacto inmutable ni cambies el binario de una línea
sin seguir el procedimiento completo anterior.
