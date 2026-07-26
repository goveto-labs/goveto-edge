# Persistent control-plane secrets

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

The generated key is stored at
`/var/lib/goveto-edge/secrets/node-credential-master.key` with mode `0600`.
Losing this file makes encrypted bootstrap identities and other stored node
secrets unreadable.

For multiple replicas, set the same `NODE_CREDENTIAL_MASTER_KEY` value on every
replica instead of relying on local files. It must be a base64-encoded 32-byte
key. The replicas then derive the same mTLS CA and gateway certificate and use
PostgreSQL leases for shared agent task delivery. The control planes also use
PostgreSQL `LISTEN`/`NOTIFY` to wake or disconnect agent sessions across
replicas. A one-second database-backed authorization and claim check remains as
a fallback if a notification is lost or a replica reconnects.

Bootstrap identities contain an agent private key. They are available only to
the cluster owner during installation and are removed from PostgreSQL when the
agent first establishes its authenticated management channel. Viewing or
downloading an identity creates an audit log entry.

Control API cookie, CSRF, authentication, request-limit, and proxy requirements
are documented in [Control API security](../../docs/control-api-security.md).
