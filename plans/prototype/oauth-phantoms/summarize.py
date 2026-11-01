import json, collections, sys

c = collections.Counter()
first = {}
for l in open(sys.argv[1]):
    r = json.loads(l)
    h = {k.lower(): v for k, v in r["h"].items()}
    a = h.get("authorization") or h.get("x-api-key") or ""
    if a.startswith("Bearer ey"):
        sig = "jwt-sig=" + a.rsplit(".", 1)[-1][:8]
    else:
        sig = a[:24]
    k = (r["m"], r["p"].split("?")[0], sig, h.get("anthropic-beta", "")[:40])
    c[k] += 1
    first.setdefault(k, r)
for k, v in c.items():
    print(v, k)
print("--- token endpoint / inference bodies")
for k, r in first.items():
    if "oauth" in k[1] or "responses" in k[1] or "messages" in k[1]:
        print(k[:2], r["b"][:300])
