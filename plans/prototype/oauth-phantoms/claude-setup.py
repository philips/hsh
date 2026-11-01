import json, time, sys, os

home = sys.argv[1]
exp_offset = int(sys.argv[2]) if len(sys.argv) > 2 else 3600
cfg = os.path.join(home, ".claude")
os.makedirs(cfg, exist_ok=True)
creds = {
    "claudeAiOauth": {
        "accessToken": "sk-ant-oat01-hsh_" + "e" * 64,
        "refreshToken": "sk-ant-ort01-hsh_" + "f" * 64,
        "expiresAt": int((time.time() + exp_offset) * 1000),
        "scopes": ["user:inference", "user:profile", "user:sessions:claude_code"],
        "subscriptionType": "max",
    }
}
p = os.path.join(cfg, ".credentials.json")
open(p, "w").write(json.dumps(creds))
os.chmod(p, 0o600)
