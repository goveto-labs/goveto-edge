# Redis key spaces

Redis stores short-lived edge state. PostgreSQL remains the source of truth.

| Pattern | Value | Purpose |
| --- | --- | --- |
| `rl:{site_id}:{ip_prefix}:{path_hash}:{window}` | Window counter | Site/IP/path rate limiting and CC protection |
| `challenge:{nonce}` | Site ID, IP prefix, UA hash, issued time | Five-second shield session; TTL is the session lifetime |
| `block:{scope}:{value}` | Site ID, reason, creator | Temporary block; TTL is the block lifetime |

Use Redis TTLs for `challenge:*` and `block:*`. Rate-limit updates must be atomic
(Lua script), and Redis failures must fail open or fall back to local limiting.
