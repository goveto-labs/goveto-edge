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

Generated purpose keys are stored under `/var/lib/goveto-edge/secrets/` with
mode `0600`. Losing these files makes the corresponding node, certificate,
DNS, notification, TOTP, or Agent CA secrets unavailable.

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

Bootstrap identities contain an agent private key. They are available only to
the cluster owner during installation and are removed from PostgreSQL when the
agent first establishes its authenticated management channel. Viewing or
downloading an identity creates an audit log entry.
