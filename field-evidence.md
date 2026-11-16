# Field evidence: message timestamps that changed on redelivery

Read-only aggregates from the 30-day `message_changes` log of five production stores
(`operation = 'upsert'`, grouped by `(chat_jid, msg_id)`). No message content, chat,
sender or phone number was read or is published. Stores A-C are sales lines, D-E are
low-traffic lines.

| Store | Upserts in 30 days | Messages whose `ts` changed | Received | Sent by the account |
|---|---|---|---|---|
| A | 20,863 | 48 | 6 | 42 |
| B | 52,794 | 144 | 143 (1 since removed) | 0 |
| C | 27,679 | 81 | 80 | 1 |
| D | 12,937 | 0 | - | - |
| E | 4,878 | 0 | - | - |

All 273 changes moved the timestamp forward; none moved it back.

For the received messages, first change only:

| Store | `ts` gap new - old, p50 / p90 / max (s) | stored time of the new upsert - its `ts`, p50 / p90 / max (s) | stored time of the first upsert - its `ts`, p50 / p90 / max (s) |
|---|---|---|---|
| A (6) | 5 / 6 / 57 | 8128 / 83918 / 410230 | 1 / 1 / 5 |
| B (143) | 7 / 56 / 2428 | 1 / 1 / 2 | 2 / 7 / 82 |
| C (80) | 6 / 103 / 846 | 1 / 1 / 3 | 2 / 3 / 14 |

Reading: in B and C, every changed `ts` equals the moment the redelivery was stored
(within 1-3 s), so a redelivery arrives stamped with its delivery time. Most are seconds
apart, with a tail of up to 40 minutes. A has only six received cases and they look
like history replays rather than live redeliveries. The log does not record whether
the content changed, so it cannot say how many of these rows were byte-identical;
the fix only applies to rows whose payload is identical.
