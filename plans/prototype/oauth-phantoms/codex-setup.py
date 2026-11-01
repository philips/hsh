import base64
import json
import os
import sys
import time

codex_home = sys.argv[1]
exp_offset = int(sys.argv[2])
port = sys.argv[3]
claims_path = sys.argv[4]


def b64(d):
    return base64.urlsafe_b64encode(json.dumps(d).encode()).rstrip(b"=").decode()


claims = {
    "email": "proto@example.com",
    "https://api.openai.com/auth": {
        "chatgpt_plan_type": "plus",
        "chatgpt_account_id": "acct-proto-123",
        "chatgpt_user_id": "user-proto-456",
    },
    "exp": int(time.time()) + exp_offset,
}
json.dump(claims, open(claims_path, "w"))
token = b64({"alg": "RS256", "typ": "JWT"}) + "." + b64(claims) + ".hsh_" + "c" * 64
auth = {
    "OPENAI_API_KEY": None,
    "tokens": {
        "id_token": token,
        "access_token": token,
        "refresh_token": "hsh_" + "d" * 64,
        "account_id": "acct-proto-123",
    },
    "last_refresh": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
}
os.makedirs(codex_home, exist_ok=True)
json.dump(auth, open(os.path.join(codex_home, "auth.json"), "w"), indent=1)
open(os.path.join(codex_home, "config.toml"), "w").write(f'''model_provider = "fake"
model = "gpt-5"
chatgpt_base_url = "http://127.0.0.1:{port}/backend-api/"
cli_auth_credentials_store = "file"

[model_providers.fake]
name = "fake"
base_url = "http://127.0.0.1:{port}/backend-api/codex"
wire_api = "responses"
requires_openai_auth = true
request_max_retries = 0
stream_max_retries = 0
''')
