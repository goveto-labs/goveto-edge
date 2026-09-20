# Goveto Edge deployment

## Quick start (Docker Compose)

```bash
cd deploy/compose
cp .env.example .env
# edit .env: set POSTGRES_PASSWORD and (for multi-replica) NODE_CREDENTIAL_MASTER_KEY
docker compose up -d
```

The stack runs TimescaleDB (control + analytics database), Redis (sessions,
rate limiting, cache) and the control plane with the console SPA and both
edge-agent architectures embedded. Open `http://localhost:8080` and follow the
instance initialization wizard. The default is local evaluation: HTTP binds
to `127.0.0.1`, `APP_ENV=development`, and cookies may use HTTP. For a remote
machine use an SSH tunnel (`ssh -L 8080:127.0.0.1:8080 user@host`) or the HTTPS
deployment below; do not expose evaluation HTTP to the public network.

Retrieve the one-time setup token locally, then enter it in the wizard:

```bash
docker compose exec control-api cat /var/lib/goveto-edge/secrets/initialization.token
```

The token is stored with mode `0600`, is never returned by the public API,
and cannot initialize an already configured instance. With multiple replicas,
supply the same `INIT_TOKEN` (base64 encoding of 32 random bytes) or
`INIT_TOKEN_FILE` to each replica during setup and remove it afterward.

## Production HTTPS

Point a public hostname at this host, allow inbound TCP 80/443 (UDP 443 for
HTTP/3), and set `CONSOLE_DOMAIN=console.example.com` in `.env`. Start the
included TLS proxy:

```bash
docker compose -f compose.yaml -f compose.https.yaml up -d
```

Caddy obtains and renews the console certificate. Open
`https://console.example.com`, complete initialization using the token above,
and sign in. Verify that `/api/v1/auth/me` succeeds after a reload and that
the session cookie has `Secure` set. The production overlay forces
`APP_ENV=production` and `SESSION_COOKIE_SECURE=true`; setting the latter to
false cannot override the production requirement. Port 8080 remains bound
only to loopback. Keep the separate mTLS agent gateway on 8443 reachable by
edge nodes. Persist `console-tls-data` along with the other volumes.

Requirements:

- Ports: 8080 (console + API), 8443 (mTLS agent gateway) must be reachable
  from edge nodes as needed; expose the API over HTTPS in production.
- Data: named volumes `pgdata`, `redisdata`, `goveto-data`, plus
  `console-tls-data` and `console-tls-config` when the HTTPS overlay is used.
  Back up `pgdata` and `goveto-data`; losing `goveto-data` loses the
  generated master keys (see below). Losing `console-tls-data` or
  `console-tls-config` makes Caddy reissue the console certificate, which can
  hit ACME rate limits.
- Upgrades: set `GOVETO_IMAGE_TAG` to the new release tag and
  `docker compose up -d`. Database schema changes are applied on startup.

Sizing note: the bundled stack targets a single host. For high availability
run multiple `control-api` replicas behind a load balancer with the same
`NODE_CREDENTIAL_MASTER_KEY` on every replica, and manage PostgreSQL/Redis
yourself.

## Persistent control-plane secrets

For a single control-plane replica, the control API generates its credential
master key on first startup. Persist `GOVETO_DATA_DIR` across replacements:

```yaml
services:
  control-api:
    environment:
      APP_ENV: production
      GOVETO_DATA_DIR: /var/lib/goveto-edge
    volumes:
      - goveto-data:/var/lib/goveto-edge

volumes:
  goveto-data:
```

Generated purpose keys are stored under `/var/lib/goveto-edge/secrets/` with
mode `0600`. Losing these files makes the corresponding node, certificate,
DNS, notification, TOTP, Agent CA, or config-secret secrets unavailable.

For multiple replicas, provide the same keys to every replica instead of
relying on local files. `NODE_CREDENTIAL_MASTER_KEY` remains the required root;
purpose-specific keys may be supplied separately as documented below. Every key
is a base64-encoded 32-byte value. The replicas then use the same mTLS CA and use
PostgreSQL leases for shared agent task delivery. The control planes also use
PostgreSQL `LISTEN`/`NOTIFY` to wake or disconnect agent sessions across
replicas. A one-second database-backed authorization and claim check remains as
a fallback if a notification is lost or a replica reconnects.

TOTP seeds use the purpose-specific `TOTP_MASTER_KEY` and bind each ciphertext
to its user ID. During rotation, configure the retired values in
`TOTP_PREVIOUS_KEYS` until startup rewrap completes on every replica.
When upgrading from a version that stored plaintext seeds, stop or drain all
old replicas before starting the new version so they cannot write plaintext
after the startup migration has completed.

Site configuration snapshots (`config_versions.config_json`), `APPLY_SITE_CONFIG`
agent task payloads, and origin pool governance seal their sensitive fields —
certificate private keys, origin mTLS keys, WAF challenge secrets, and ACME
key authorizations — with the purpose-specific `CONFIG_SECRET_MASTER_KEY`,
each ciphertext bound to its scope (site and version for snapshots, cluster
and origin pool for governance). Rows written before sealing, or by a previous
key, are resealed during startup rewrap; unmigratable rows are skipped with a
warning instead of blocking startup. Rotate via `CONFIG_SECRET_PREVIOUS_KEYS`
until that rewrap completes on every replica. Losing this key makes sealed
snapshots unreadable, which blocks republishing those versions (and rolls
such sites back to a disabled tombstone), so back it up together with
`goveto-data`.

Superseded snapshots are pruned hourly: each site keeps its live version, its
newest published or rolled-back baseline, and the twenty most recent terminal
versions. Draft snapshots pending publication are never pruned.

Bootstrap identities contain an agent private key. They are available only to
the cluster owner during installation and are removed from PostgreSQL when the
agent first establishes its authenticated management channel. Viewing or
downloading an identity creates an audit log entry.
