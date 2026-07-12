# Persistent control-plane secrets

The control API generates its node credential master key on first startup.
Persist `GOVETO_DATA_DIR` across container replacements:

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
Losing this file makes existing encrypted node communication keys unreadable.

The key path is intentionally fixed relative to `GOVETO_DATA_DIR`; there is no
separate environment variable for the key itself or its filename.
