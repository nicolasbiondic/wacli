# wacli #528 — data-model compatibility proof

Populated store written by stock v0.25.0, upgraded with the #528 branch, then reopened with stock v0.25.0 (read-only and writable). Offline, synthetic data, no account.

- `upgrade.mp4` / `upgrade.gif`: terminal recording (`upgrade.cast` is the asciinema source).
- `upgrade-transcript.txt`: the same output as text.
- `upgrade-proof.sh`, `fingerprint.py`: what was run.
- `fp-before.txt`, `fp-after.txt`, `fp-downgrade.txt`: per-table row counts and content hashes (the new nullable `kind` column excluded and reported separately).
