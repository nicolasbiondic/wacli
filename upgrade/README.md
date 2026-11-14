# Actual released v0.26.0 owner upgrade trace

Candidate head remains `8c34a44335e1`; no PR code or commit changed.
This follow-up replaces the earlier modeled-owner limitation with an actual
released-binary owner run on one disposable store.

## What the stable owner is

The executable is the official Linux amd64 v0.26.0 release, verified against
the executable extracted from the release tarball, whose checksum is rechecked
against the adjacent official `checksums.txt`.

Official binary sha256=3750e566e88a3a7ae47e485c02392ad60b972160fc85e5bd789e896673245f76

The owner runs a temporary copy with only the 32-byte WhatsApp Noise root public
key replaced by an ephemeral test CA public key. **It is not byte-identical.**
The test verifies one occurrence of the old root, identical file size, 32
differing bytes in the recorded run, all inside that single 32-byte window,
and byte equality everywhere outside it. An ELF check confirms the window is
in a non-executable data section. Every instruction, CLI path, store writer and
IPC version guard is unchanged. No modeled guard or rebuilt stable executable
is used.

## Offline isolation

The child gets an isolated HOME and a fresh environment with no WACLI variables.
HTTPS_PROXY and HTTP_PROXY point at a local CONNECT proxy, NO_PROXY is empty,
and SSL_CERT_FILE/SSL_CERT_DIR select only the test TLS CA and an empty directory.
The TLS peer has a test-CA certificate for `web.whatsapp.com`.

The proxy accepts only CONNECT to `web.whatsapp.com:443`; its upstream is the
in-process loopback TLS/WebSocket peer. It checks both child and upstream TCP
addresses are loopback. The final assertion records exactly one approved
CONNECT and no other targets. The candidate in-process client also has a
fail-closed dial redirected only to that peer. The replacement Noise root
would reject a real-service Noise certificate even if it could reach one.

## Observed sequence

1. The unmodified released binary runs `auth status`, creating the disposable
   store. A synthetic already-paired device and real Signal keys are then added
   only to the test session database.
2. The released copy runs `sync --follow` as a real subprocess, connects and
   starts its own AfterConnect IPC listener. The peer delivers two encrypted
   messages to A and one to B. The released writer persists A=2 and B=1.
3. The candidate's actual Cobra keep-unread command meets that owner's lock.
   The **released executable's own guard** returns unsupported delegate version
   2. Cobra prints the upgrade/restart explanation and "not sent". A remains 2,
   B remains 1, and zero message stanzas appear: no policy-free fallback.
4. A candidate ordinary send to B delegates as v1. The released owner encrypts
   it; the peer decrypts the synthetic text and acknowledges it. B becomes 0;
   A remains 2.
5. Only the subprocess PID created by the test receives SIGTERM. Its clean exit
   is awaited; lock reacquisition/release proves the lock is free. The candidate
   follow owner opens the **same store**, with A still 2.
6. The real candidate v2 keep-unread send is encrypted and acknowledged. A stays
   2, including after an own-device encrypted echo. A subsequent ordinary send
   clears A to 0.
7. The actual released copy also runs as a CLI client, delegating an ordinary
   v1 send to B through the candidate owner. It succeeds without opening another
   WebSocket connection.

Every peer stanza and every requested store state is logged. State inspection
uses mode=ro, query-only SQLite connections, never writes.
The published full verbose output retains every line, with redaction and
trailing-whitespace normalization. The harness source is an unchanged byte copy.
All four accepted sends are actually encrypted, decrypted and acknowledged;
there are no read receipts or outbound app-state patches.

## Reproduce

Use a detached checkout of candidate `8c34a44335e1`, which already contains
`send_keep_unread_transport_test.go`. Copy the adjacent harness text into
`cmd/wacli/send_stable_upgrade_transport_test.go`, without changing any existing
file. Set RELEASE to the verified official v0.26.0 executable; keep its release
tarball and `checksums.txt` beside it.

```sh
WACLI_STABLE_PROOF_BINARY="$RELEASE" go test -tags sqlite_fts5 -run '^TestStableOwnerUpgradeTransport$' -count=1 -v github.com/openclaw/wacli/cmd/wacli
```

The recorded run uses Go 1.27.4. The FTS5 tag is required because the official
release creates an FTS5-backed store; an untagged candidate cannot update that
schema. The FTS trace and tagged `go vet` pass. No PR gate is rerun or claimed for
modified PR source: the PR head stays unchanged and its earlier full gate stands.

The extra harness is out of tree, not part of the PR. Its exact source is
published alongside the full verbose output, including the official and
temporary-copy checksums. Temporary patched executables are deleted by test
cleanup, the original release is checked unchanged, and only synthetic stores
are used. The detached source/proof worktrees are removed after publication.

Not proven: pairing/login bootstrap, remote WhatsApp service policy, phone UI,
or behavior of a completely unmodified trust anchor against a real service.
No production store, account, endpoint or session is involved.

Nicolás (via agent)
