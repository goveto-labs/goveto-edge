# Data-plane E2E tests

These tests drive the **real edge agent as a black box** over the production
mTLS management protocol. There is no PostgreSQL, Redis or control API in the
loop — instead a stub control-plane gateway (`harness.go`) issues the exact
`APPLY_SITE_CONFIG` / `PURGE_SITE` / `NODE_CACHE_CONFIG` tasks the real control
plane would, and the agent applies them to its embedded Caddy instance and
serves real HTTP traffic.

This is deliberately one layer above the white-box tests in
`internal/edgeagent` (which call `ConfigManager.ApplySite` directly). Here the
only handle on the agent is the network: the gRPC + JSON task codec, the mTLS
handshake, and the listener port.

## Architecture

```
  test goroutine
       │  sendTask (APPLY_SITE_CONFIG / PURGE_SITE)
       ▼
  stubGateway  ──mTLS gRPC──►  edgeagent.Agent  ──►  embedded Caddy  ──►  origin
       ▲                           ▲
       │  task result              │  HTTP request/response (MISS/HIT/purge)
       └───────────────────────────┘
```

`newHarness` wires:

1. A real `edgecontrol.Authority` (CA + server cert pinned to `127.0.0.1`).
2. A real node identity via `authority.IssueNode`, written to the agent's
   identity file.
3. A stub gRPC gateway implementing `edgeprotocol.ManagementServer` that
   welcomes the agent, acks every log batch (no log backpressure), and delivers
   tasks while collecting each result so a test never probes traffic before the
   config has actually been applied.
4. The real `edgeagent.New()` agent, started with env vars pointing at the temp
   identity / data / cache dirs.

## Running

```sh
go test ./tests/e2e/ -v
go test ./tests/e2e/ -race        # concurrency-safe harness
```

Each test is self-contained: it brings up its own agent, origin and gateway and
tears them down on completion. No external services are required.

## Coverage

| Scenario | Test | Notes |
|---|---|---|
| Origin → create site → publish → MISS → HIT → purge → MISS | `TestCacheLifecycleE2E` | The minimum viable data-plane link. |
| Multi-domain HTTPS | `TestMultiDomainHTTPSServesAllSANs` | One cert, multiple SANs, SNI routing. |
| Multi-origin weight + health-check failover | `TestMultiOriginFailoverE2E` | Primary 503 → failover to backup → recovery. |
| WAF block | `TestWAFBlocksMaliciousRequestE2E` | Custom rule group blocks before the origin; benign traffic passes. |
| Range / large file | `TestRangeLargeFileE2E` | Byte-range served from the cached full object. |
| Control-plane restart → Job recovery | `TestControlPlaneRestartResumesJobs` | Gateway torn down and rebuilt; agent reconnects and accepts a fresh publish. |

## Out of scope (require the full control plane)

These scenarios hinge on control-plane behaviour (the publisher's multi-node
fan-out, the DNS reconciler, Redis-backed rate limits) and are not exercisable
through the data plane alone. They are covered by the component tests below and
a future black-box stack test (docker-compose + control API) would close the
remaining gap:

| Scenario | Where it is tested today |
|---|---|
| Multi-node partial-failure rollback | `internal/publisher/service_test.go` (fan-out + rollback) |
| Node-offline DNS removal | `internal/dnssync/service_test.go` (grace-period reconciliation) |
| Rate limiting | `internal/policy` + `internal/edgeagent/configmanager_*_e2e_test.go` (Redis backend required) |

## Adding a scenario

1. Call `newHarness(t)` to get a running agent + gateway + free HTTP port.
2. Spin up one or more `httptest.Server` origins.
3. Build an `edgeprotocol.SiteConfig` and call `h.applySite(ctx, config)` — this
   blocks until the agent acknowledges the task.
4. Drive traffic with `h.request(...)` and assert on `X-Cache` / status / body.
5. Purge with `h.purge(ctx, edgeprotocol.PurgeRequest{...})`.

The harness pins the cache directory to a temp dir with a huge fake capacity
(`cachefs.OverrideDiskUsageForTesting`) so cache writes never reject in a
sandboxed CI runner.
