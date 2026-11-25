# Answering calls on the sync daemon's session (experimental)

`wacli calls serve` (see `calls-answer-lab.md`) needs a session of its own: it
cannot run next to `wacli sync --follow` on the same store, and linking it
takes one of the four linked devices that may receive calls. This branch lets
the sync daemon itself answer calls on the session it already holds.

**Ban risk.** Answering calls with an unofficial client may get the number
banned. Lab numbers first; a production line only with the owner's written
approval and `--calls-allow` restricted to the team.

## Flags (`wacli sync --follow`)

| Flag | Default | Meaning |
|---|---|---|
| `--calls off\|manual\|auto` | `off` | `off` installs nothing: the daemon behaves exactly as before. `manual` engages allowed callers and waits for `wacli calls answer`. `auto` answers them after the delay with the audio below. There is no `reject` mode: declining from this device would be this device's decision for the whole line |
| `--calls-allow` | everyone | numbers or JIDs (comma separated or repeated). Any other caller is **not engaged at all**: no preaccept, no relay traffic, no events but `call_ignored`; the call rings on the phone and the other devices exactly as without the engine. LID callers match through the session's LID→phone map |
| `--calls-answer-delay` | `2s` | wait before auto-answering |
| `--calls-audio-in` / `--calls-audio-out` | `silence` / `none` | audio for auto-answered calls: file, `rtp://host:port`, `rtp://auto:0`, `echo` |
| `--calls-max-duration` | `10m` | hang up answered calls after this long (`0` = never) |
| `--calls-max-concurrent` | `1` | calls answered at once; more keep ringing on the other devices (`call_busy`) |
| `--calls-ring-timeout` | `2m` | forget a ringing call that never got an end |
| `--calls-log-level` | `info` | engine log level (stderr) |

Every `--calls-*` flag is refused unless `--calls` is `manual` or `auto`, and
`--calls` is refused outside follow mode.

## Control and events

The daemon opens `<store>/calls.sock` (0600). The existing commands talk to it:

```
wacli --store <store> calls active
wacli --store <store> calls answer --call-id <id> --audio-in rtp://0.0.0.0:40000 --audio-out rtp://auto:0
wacli --store <store> calls hangup --call-id <id>
wacli --store <store> calls watch        # NDJSON: {"event","data","ts"} per line
```

Events: `call_incoming`, `call_media_ready`, `call_answered`,
`call_playback_finished`, `call_ended` (reason, duration, RTP counters),
`call_ignored` (not allowed, capacity), `call_busy`, `call_error`,
`call_engine_panic`. With `--events` they also go to stderr as NDJSON;
otherwise as `calls: <event> k=v` lines. A `watch` client that falls behind
loses lines (the next line carries `"dropped": n`) instead of slowing the
daemon. At most 8 watchers.

`hangup` of a ringing call on the shared session only drops it here; the
phone keeps ringing. Of an answered call, it ends the call.

## Isolation

- The engine (meowcaller, `lenz/daemon-isolation`) runs every goroutine and
  every whatsmeow hook under `recover`. A panic is logged, reported as
  `call_engine_panic`, and live calls are hung up; the session, message
  receipt and sends keep running. whatsmeow keeps calling the sync handlers
  registered after the engine.
- The service guards its own callbacks, timers and control requests the same
  way; a request over 64 KiB is refused.
- Caps: answered calls (`--calls-max-concurrent`), tracked calls (32), ring
  timeout, max duration, watcher count and per-watcher buffer.
- On exit the daemon hangs up live calls, closes audio bridges and timers,
  ends watchers and removes the socket, even if the engine never confirms.
- The call stanzas still reach wacli's `call_events` table as before.

## Lab procedure

1. Lab number already linked in its store (`wacli --store <lab> auth`).
2. `wacli --store <lab> sync --follow --calls auto --calls-allow <team phone>
   --calls-audio-in echo --calls-audio-out echo --calls-log-level debug`
3. Send a message to the lab number and check it is stored; call it from the
   team phone: the echo must be heard; `calls watch` shows incoming →
   answered → ended; send and receive messages during the call.
4. Call from a number outside `--calls-allow`: it must ring on the lab phone
   only, with a single `call_ignored`.
5. Repeat with `--calls manual` and `calls answer`/`calls hangup`.
