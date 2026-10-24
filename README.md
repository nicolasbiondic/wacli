# wacli #528 — data-model compatibility proof

Populated store written by stock v0.25.0, upgraded with the #528 branch, then reopened with stock v0.25.0 (read-only and writable). Offline, synthetic data, no account.

- `upgrade.mp4` / `upgrade.gif`: terminal recording (`upgrade.cast` is the asciinema source).
- `upgrade-transcript.txt`: the same output as text.
- `upgrade-proof.sh`, `fingerprint.py`: what was run.
- `fp-before.txt`, `fp-after.txt`, `fp-downgrade.txt`: per-table row counts and content hashes (the new nullable `kind` column excluded and reported separately).

# Live behavior proof (real account)

A companion linked by phone code to a real account, running this branch's `sync --follow` (stock whatsmeow pin) with `--webhook` to a local listener. A poll arrived from another phone; a vote, a contact card and an event were sent from the primary. Phone numbers, JIDs and the contact's name are redacted.

- `live.mp4` / `live.gif` / `live.cast`: recording; `live-transcript.txt`: the same output as text.
- `live-proof.sh`, `rows.py`: what was run.
