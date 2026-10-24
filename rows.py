import json, sys
d = json.load(sys.stdin)["data"]
msgs = d.get("messages", d) if isinstance(d, dict) else d
if isinstance(msgs, dict):
    msgs = [msgs]
for m in msgs:
    mid = (m.get("MsgID") or "")[:12]
    text = (m.get("DisplayText") or m.get("Text") or "").replace("\n", " ")[:48]
    print(f"{mid}…  from_me={str(m.get('FromMe')).lower():5s}  kind={m.get('kind', '(absent)'):10s}  media_type={m.get('MediaType') or '':6s}  {text}")
