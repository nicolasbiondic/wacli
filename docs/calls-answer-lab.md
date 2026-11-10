# Answering voice calls (experimental, lab only)

`wacli calls serve` answers WhatsApp voice calls using
[meowcaller](https://github.com/purpshell/meowcaller) (MIT, pure Go, MLow codec).
It is **experimental** and meant for a dedicated lab number in its own store:
answering calls through an unofficial client is a ban risk, and whatsmeow is
hooked through reflection, so a whatsmeow update can break it silently.
Never run it on a line that talks to customers.

The build pins `github.com/nicolasbiondic/meowcaller@lenz/answer-stack`
(`go.mod` replace): upstream `27a3c6b` (the last revision on
`go.mau.fi/whatsmeow`) plus the community fixes that make *incoming* calls
carry audio in both directions (#26 bind every offered relay, #43 start media
on the offer so a human can answer seconds later, #47 ignore rejects from our
own devices, #45 keep inbound audio in sequence, #50 survive an error ack on
the accept). Upstream meowcaller has not merged them.

## Commands

| Command | What it does |
|---|---|
| `wacli calls serve` | Connects the store and waits for calls. Own process: do not run `wacli sync` on the same store at the same time. |
| `wacli calls answer --call-id … --audio-in … --audio-out …` | Answers a ringing call in that `serve`. |
| `wacli calls hangup --call-id …` | Hangs up (or rejects a ringing call). |
| `wacli calls active` | Ringing, active and recently ended calls, with RTP counters. |

`serve` flags: `--answer manual|auto|reject` (default manual), `--allow` (phone
numbers or JIDs that may be auto-answered), `--answer-delay` (default 2s),
`--audio-in` / `--audio-out` used for auto answers, `--max-duration` (default
10m), `--log-level`. The control socket is `<store>/calls.sock`.

Audio endpoints (all 16 kHz mono):

| Spec | `--audio-in` (what the caller hears) | `--audio-out` (caller's voice) |
|---|---|---|
| `silence` / `none` | silence | discarded |
| `echo` (both sides) | the caller hears themselves back | fed back |
| `file.wav` / `.mp3` / `.opus` / `.pcm` | played once, then silence | — |
| `rec.wav` | — | recorded (16 kHz s16 mono) |
| `rtp://host:port[?pt=118]` | bind and receive L16/16000 RTP | send L16 RTP to host:port |
| `rtp://auto:0` | — | send back to whoever sent us RTP (symmetric; needs an `rtp://` in) |

RTP is linear PCM (L16, RFC 3551) 16 kHz mono, 20 ms packets, dynamic payload
type 118 by default. In Asterisk that is `slin16`; an ARI `externalMedia`
channel with `format=slin16` talks to it directly.

## Lab run

1. Build: `go build -o wacli ./cmd/wacli`.
2. Link the lab number in a store of its own (never an existing one):
   ```sh
   wacli --store /tmp/opencode/wa-lab auth
   ```
   Scan the QR from the lab phone (Linked devices → Link a device).
3. Start the answering process:
   ```sh
   wacli --store /tmp/opencode/wa-lab calls serve --answer auto \
     --audio-in echo --audio-out echo --log-level debug
   ```
4. From another phone, call the lab number. Expected log sequence:
   `incoming call` → `starting media` → `relay fanout established` →
   `first RTP decoded from relay, inbound audio flowing` → `accepted` →
   `relay is bridging the peer's media`. Speak: you should hear yourself with
   a short delay. `call_ended` prints duration and RTP counters.
5. Manual answer and recording:
   ```sh
   wacli --store /tmp/opencode/wa-lab calls serve --answer manual
   # on the ring:
   wacli --store /tmp/opencode/wa-lab calls active
   wacli --store /tmp/opencode/wa-lab calls answer --call-id <id> \
     --audio-in hello.mp3 --audio-out /tmp/opencode/wa-lab/caller.wav
   ```
6. RTP bridge check without Asterisk (two shells):
   ```sh
   # answers with RTP bound on 4000 and replies to the sender
   wacli --store /tmp/opencode/wa-lab calls serve --answer auto \
     --audio-in rtp://0.0.0.0:4000 --audio-out rtp://auto:0
   # a peer that echoes RTP back, e.g. with ffmpeg or a small socat loop,
   # or point --audio-out at a host:port running `ffplay -f rtp`.
   ```

## What to expect / known limits

- Only 1:1 audio calls. Group calls and video are not wired.
- Inbound audio depends on relay election: if the caller sits in another
  region than this server, consider porting meowcaller PR #49 (relay
  steering) on top of the stack.
- WhatsApp Business numbers onboarded to the Cloud API ("coexistence") reject
  from a hosted device and need `platform`/`version` on the accept
  (meowcaller issue #35); not included.
- Each call's session is logged by meowcaller with `--log-level debug`; redact
  JIDs before sharing logs.
