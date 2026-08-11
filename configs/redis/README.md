# Redis key spaces

Redis stores short-lived edge state. PostgreSQL remains the source of truth.

| Pattern | Value | Purpose |
| --- | --- | --- |
| `rl:{site_id}:{rule_id}:{value_digest}` | Integer | Atomic WAF `RATE_LIMIT` rule window counter |
| `challenge:{token_digest}` | `issued` | Single-use CAPTCHA challenge state |

Digests are the first 16 bytes of SHA-256 encoded as lowercase hexadecimal.
Every key has a TTL. WAF rate counters are updated atomically by Lua and fall
back to a bounded local counter when Redis is unavailable. Challenge state is
atomically consumed to reject replay. CAPTCHA state fails open to signed
stateless tokens when Redis is unavailable.
