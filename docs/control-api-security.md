# Control API security

## Browser and cookie policy

Session cookies are `HttpOnly`, `SameSite=Lax`, and use the `Secure` flag when
`APP_ENV=production`. Production mode ignores an explicit
`SESSION_COOKIE_SECURE=false` value.

Authenticated browser mutations use double-submit CSRF protection. Login sets
a readable `<SESSION_COOKIE_NAME>_csrf` cookie. For every authenticated
`POST`, `PUT`, `PATCH`, or `DELETE`, the client must copy its value into the
`X-CSRF-Token` request header. The server compares the two values in constant
time. It also rejects cross-site Fetch Metadata and an `Origin` that does not
match the request origin. `GET`, `HEAD`, and `OPTIONS` are exempt.

Requests without a session cookie do not require a CSRF token, so initialization,
login, registration, and password-reset clients can operate before a session
exists. Their rate limits still apply. A non-browser client that sends a session
cookie must also return the CSRF cookie and header. If the CSRF cookie goes
missing while the session remains valid, the next authenticated request
reissues it, so a session can always recover its ability to mutate.

TLS-terminating proxies must overwrite `X-Forwarded-Proto` with the externally
observed scheme. Do not pass a client-supplied value through unchanged.

## Authentication protection

Rate limits are fixed-window counters in Redis and are keyed by the direct
client IP. When the API runs behind a reverse proxy, set
`HTTP_TRUSTED_PROXIES` to a comma-separated list of proxy IPs or CIDRs; the
client IP is then resolved from `X-Forwarded-For` while trusting only those
proxies. Without this setting every client shares the proxy's address, which
turns the per-IP limits below into global ones. The API fails closed with
`503` when Redis cannot enforce a limit.

| Endpoint | Limit |
| --- | --- |
| `POST /api/v1/auth/login` | 10/minute |
| `POST /api/v1/auth/register` | 5/hour |
| `GET /api/v1/auth/registration-config` | 60/minute |
| `GET /api/v1/init/status` | 60/minute |
| `POST /api/v1/init` | 5/hour |
| `POST /api/v1/auth/password-reset` | 5/hour |
| Password change and TOTP credential mutations | 10/minute per user |

The per-user limit covers every endpoint that verifies the current password,
so a hijacked session cannot brute-force the account password online.

Login failures have a 500 ms minimum delay that increases with repeated
failures. Five failures lock an active account for 15 minutes. Success clears
the failure state. A login that omits a required TOTP code receives the code
prompt without counting toward the lockout, since the two-step browser flow
lands there on every login. Login, registration, initialization, password, TOTP,
security-policy, and session mutations are included in the audit log; secrets,
passwords, tokens, and one-time codes are redacted.

## TOTP, passwords, and sessions

The authentication API exposes these security-management operations:

| Endpoint | Purpose |
| --- | --- |
| `POST /api/v1/auth/totp/setup` | Generate an enrollment secret and URI |
| `POST /api/v1/auth/totp/enable` | Verify password and TOTP, then return recovery codes once |
| `POST /api/v1/auth/totp/reset` | Verify the old factor and start replacement enrollment |
| `POST /api/v1/auth/totp/recovery-codes` | Replace recovery codes after password and factor verification |
| `DELETE /api/v1/auth/totp` | Disable TOTP when the global policy permits it |
| `GET, PUT /api/v1/auth/security-policy` | Read or administratively set mandatory TOTP enrollment |
| `PUT /api/v1/auth/password` | Change the current password |
| `POST /api/v1/auth/password-reset/admin-token` | Administratively issue a 30-minute reset token |
| `POST /api/v1/auth/password-reset` | Consume a reset token and set a new password |
| `GET /api/v1/auth/sessions` | List active sessions and their metadata |
| `DELETE /api/v1/auth/sessions/{id}` | Revoke one owned session |
| `POST /api/v1/auth/sessions/revoke-others` | Revoke all sessions except the current one |

Recovery codes and password-reset tokens are stored only as SHA-256 hashes.
Recovery codes are single-use, and accepted TOTP codes are consumed so a
captured code cannot be replayed within its validity window. Enabling TOTP
while it is already enabled returns `409`; replacing the secret must go
through reset, which verifies the current second factor. Password reset
revokes every session; password and TOTP credential changes revoke other
sessions. When mandatory TOTP is enabled, an unenrolled user can access only
profile, logout, setup, and enable operations until enrollment is complete.

Sessions are recorded in PostgreSQL and validated on every request. Sessions
created before this mechanism existed fail that validation, so upgrading to it
signs every user out once.

No mail delivery system currently exists, so password-reset tokens are issued
by an authenticated administrator and returned once in the response.

## Transport and request limits

The server sends CSP, clickjacking, MIME-sniffing, referrer, permissions, and
cross-origin isolation headers on every response. API responses additionally
send `Cache-Control: no-store`. Production responses also send one-year HSTS.

Defaults can be overridden with environment variables:

| Variable | Default |
| --- | --- |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` |
| `HTTP_READ_TIMEOUT` | `15s` |
| `HTTP_WRITE_TIMEOUT` | `30s` |
| `HTTP_IDLE_TIMEOUT` | `60s` |
| `HTTP_MAX_HEADER_BYTES` | `32768` |
| `HTTP_MAX_BODY_BYTES` | `2097152` |
| `HTTP_MAX_UPLOAD_BYTES` | `16777216` |
| `HTTP_TRUSTED_PROXIES` | unset (client IP is the direct peer) |

Requests are also limited to 100 distinct header names. Multipart requests use
the upload limit; other requests use the body limit. Every response includes a
normalized `X-Request-ID`. Panic recovery and structured access logs include
that ID, method, matched route, status, duration, response bytes, direct client
IP, and user agent.
