# Live proof: quick replies on a WhatsApp Business account (2026-10-27)

Build `0.25.0` from `upstream/quick-replies-v2` @ 9b0d6b3 on a temporary companion (own store, paired by phone code), `sync --follow` owning the store; all writes delegated to it (`app_state_write`). Phone observations by the account owner. Pre-existing replies' shortcuts and text replaced by placeholders; phone number masked.

### `app-state resync regular`
```json
{
 "success": true,
 "data": {
  "collection": "regular",
  "completeness": "complete",
  "quick_replies": 3,
  "lists": 4
 },
 "error": null
}
```

### `quick-replies list` after resync
```json
{
 "success": true,
 "data": {
  "quick_replies": [
   {
    "id": "B16FABFC-EBE2-4F11-967A-B7B3E98BDADE",
    "shortcut": "<existing-1>",
    "message": "<redacted>",
    "keywords": [],
    "label_ids": [],
    "count": 0
   },
   {
    "id": "1D160D9A-E89F-4D1B-81D4-E0EAE73164AC",
    "shortcut": "<existing-2>",
    "message": "<redacted>",
    "keywords": [],
    "label_ids": [],
    "count": 4
   },
   {
    "id": "F4784427-EAC2-4C52-95BC-A6361FE15B87",
    "shortcut": "<existing-3>",
    "message": "<redacted>",
    "keywords": [],
    "label_ids": [],
    "count": 17
   }
  ],
  "completeness": "complete"
 },
 "error": null
}
```

### `quick-replies create --shortcut prueba` — appeared on the phone
```json
{
 "success": true,
 "data": {
  "action": "quick_reply_create",
  "ok": true,
  "quick_reply": {
   "id": "1793146365",
   "shortcut": "prueba",
   "message": "Prueba Vendas (crear)",
   "keywords": [
    "prueba"
   ],
   "label_ids": [],
   "count": 0
  }
 },
 "error": null
}
```

### `quick-replies edit prueba --message …` — phone showed the new text
```json
{
 "success": true,
 "data": {
  "action": "quick_reply_edit",
  "ok": true,
  "quick_reply": {
   "id": "1793146365",
   "shortcut": "prueba",
   "message": "Prueba Vendas (editada)",
   "keywords": [
    "prueba"
   ],
   "label_ids": [],
   "count": 0
  }
 },
 "error": null
}
```

### `quick-replies delete prueba` — gone from the phone
```json
{
 "success": true,
 "data": {
  "action": "quick_reply_delete",
  "ok": true,
  "quick_reply": {
   "id": "1793146365",
   "shortcut": "prueba",
   "message": "Prueba Vendas (editada)",
   "keywords": [
    "prueba"
   ],
   "label_ids": [],
   "count": 0,
   "deleted": true
  }
 },
 "error": null
}
```

### list after delete
```json
{
 "success": true,
 "data": {
  "quick_replies": [
   {
    "id": "B16FABFC-EBE2-4F11-967A-B7B3E98BDADE",
    "shortcut": "<existing-1>",
    "message": "<redacted>",
    "keywords": [],
    "label_ids": [],
    "count": 0
   },
   {
    "id": "1D160D9A-E89F-4D1B-81D4-E0EAE73164AC",
    "shortcut": "<existing-2>",
    "message": "<redacted>",
    "keywords": [],
    "label_ids": [],
    "count": 4
   },
   {
    "id": "F4784427-EAC2-4C52-95BC-A6361FE15B87",
    "shortcut": "<existing-3>",
    "message": "<redacted>",
    "keywords": [],
    "label_ids": [],
    "count": 17
   }
  ],
  "completeness": "complete"
 },
 "error": null
}
```

### reply `/desdetel` created **on the phone**, then listed
```json
{
 "success": true,
 "data": {
  "quick_replies": [
   {
    "id": "60FD5EE5-1EA9-424D-9C32-19EE68C9094D",
    "shortcut": "desdetel",
    "message": "Prueba desde el teléfono",
    "keywords": [],
    "label_ids": [],
    "count": 0
   },
   {
    "id": "B16FABFC-EBE2-4F11-967A-B7B3E98BDADE",
    "shortcut": "<existing-1>",
    "message": "<redacted>",
    "keywords": [],
    "label_ids": [],
    "count": 0
   },
   {
    "id": "1D160D9A-E89F-4D1B-81D4-E0EAE73164AC",
    "shortcut": "<existing-2>",
    "message": "<redacted>",
    "keywords": [],
    "label_ids": [],
    "count": 4
   },
   {
    "id": "F4784427-EAC2-4C52-95BC-A6361FE15B87",
    "shortcut": "<existing-3>",
    "message": "<redacted>",
    "keywords": [],
    "label_ids": [],
    "count": 17
   }
  ],
  "completeness": "complete"
 },
 "error": null
}
```

### `quick-replies delete desdetel` (phone-created, UUID id)
```json
{
 "success": true,
 "data": {
  "action": "quick_reply_delete",
  "ok": true,
  "quick_reply": {
   "id": "60FD5EE5-1EA9-424D-9C32-19EE68C9094D",
   "shortcut": "desdetel",
   "message": "Prueba desde el teléfono",
   "keywords": [],
   "label_ids": [],
   "count": 0,
   "deleted": true
  }
 },
 "error": null
}
```
