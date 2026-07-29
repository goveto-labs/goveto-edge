# Guide for Generated GCO Client

This document is generated automatically by `gco generate`.

## About GCORM

GCORM is a schema-first ORM toolkit for Go. Define models in `.gcorm` files (Prisma-like syntax), run the `gco` CLI, and use the generated type-safe `client`, `query`, and `model` packages in your application.

It covers code generation, query builders, migration planning, and database tooling.

For full documentation, advanced guides, and the latest reference, visit the
[GCORM Wiki](https://github.com/arsfy/gcorm/wiki).

## gco CLI Basics

The GCORM CLI is `gco`:

```sh
gco <command> [flags]
```

Common workflow:

```sh
# Initialize a schema (default: schema/schema.gcorm)
gco init

# Validate and format schema files
gco validate
gco fmt

# Generate Go client code (also writes this GUIDE.md)
gco generate
gco generate --schema schema
gco generate --dry-run

# Push schema changes to the database (dev)
gco db push

# Create / inspect migrations
gco migrate diff --name init
gco migrate init-sql --output init.sql
```

Useful flags (accepted by many commands):

- `--schema <path>` — schema directory or file
- `--config <path>` — `gco.config.yaml` / `gco.config.yml`

Other commands: `gco version`, `gco upgrade`, `gco help`.
See the [CLI Reference](https://github.com/arsfy/gcorm/wiki/CLI-Reference) on the wiki for the full list.

## Imports

```go
import (
    "context"
    "database/sql"
    "your-module/gen/client"
    "your-module/gen/query"
    "your-module/gen/model"
)
```

## Client Setup

```go
db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
if err != nil {
    panic(err)
}
defer db.Close()

c := client.New(db, client.WithDialect("postgresql"))
defer c.Close()

ctx := context.Background()
```

## Recommended Usage Pattern

Prefer staged builders for readability and composability:

```go
users, err := c.User.Query().
    Where(query.User.Email.Contains("@example.com")).
    OrderBy(query.User.CreatedAt.Desc()).
    Take(20).
    Do(ctx)
```

## CRUD Builder Pattern

- Create one: `c.<Model>.Create().Set(...).Do(ctx)`
- Update one: `c.<Model>.Update().Where(...).Set(...).Do(ctx)`
- Update many: `c.<Model>.Update().Where(...).Set(...).DoMany(ctx)`
- Delete one: `c.<Model>.Delete().Where(...).Do(ctx)`
- Delete many: `c.<Model>.Delete().Where(...).DoMany(ctx)`

## Bulk Inserts

Use `BulkCreate` for high-volume inserts, idempotent sync jobs, and join table writes:

```go
rows, err := c.Post.BulkCreate(posts).
    OnConflictDoNothing(query.PostIdColumn).
    Returning(query.PostIdColumn).
    BatchSize(1000).
    DoReturningValues(ctx)
```

- `Do(ctx)` executes batches and returns affected rows.
- `DoReturning(ctx)` returns inserted model rows on PostgreSQL.
- `DoReturningValues(ctx)` returns selected `RETURNING` columns as row maps.
- `CreateMany(ctx, data)` remains available and delegates to `BulkCreate(data).Do(ctx)`.

## Raw SQL

Use raw SQL for CTEs, `UPDATE ... FROM`, query-plan-sensitive joins, and PostgreSQL-specific features:

```go
posts, err := client.Raw[model.Post](ctx, c,
    `SELECT id, title, author_id FROM posts WHERE id = $1`, id)
```

- `client.Raw[T](ctx, c, sql, args...)` scans rows into a struct using `db` tags.
- `c.RawRows(ctx, sql, args...)` returns `[]map[string]any` for ad-hoc projections.
- `c.RawExec(ctx, sql, args...)` executes custom SQL and returns affected rows.

## Table and Column Constants

Generated query packages include constants for hand-written SQL:

```go
query.PostTable
query.PostIdColumn
query.PostAuthorIdColumn
```

## Models and Fields

### ACMEAccount

- Client handle: `c.ACMEAccount`
- Query namespace: `query.ACMEAccount`
- Fields:
  - `Id` (String, id)
  - `Clusterid` (String)
  - `Directoryurl` (String)
  - `Email` (String)
  - `Privatekeyencrypted` (String)
  - `Accountjson` (Json)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### ACMEChallenge

- Client handle: `c.ACMEChallenge`
- Query namespace: `query.ACMEChallenge`
- Fields:
  - `Id` (String, id)
  - `Certificateid` (String)
  - `Clusterid` (String)
  - `Type` ()
  - `Status` ()
  - `Domain` (String)
  - `Token` (String)
  - `Keyauth` (String, optional)
  - `Dnsname` (String, optional)
  - `Dnsvalue` (String, optional)
  - `Providerref` (String, optional)
  - `Lasterror` (String, optional)
  - `Expiresat` (DateTime)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### AgentTask

- Client handle: `c.AgentTask`
- Query namespace: `query.AgentTask`
- Fields:
  - `Id` (UUID, id)
  - `Nodeid` (UUID)
  - `Kind` (String)
  - `Payload` (Json)
  - `Status` ()
  - `Leaseowner` (String, optional)
  - `Leaseuntil` (DateTime, optional)
  - `Heartbeatat` (DateTime, optional)
  - `Attempts` (Int)
  - `Maxattempts` (Int)
  - `Nextattemptat` (DateTime)
  - `Idempotencykey` (String, optional, unique)
  - `Cancelrequestedat` (DateTime, optional)
  - `Timeoutat` (DateTime, optional)
  - `Resultjson` (Json, optional)
  - `Compensationjson` (Json, optional)
  - `Error` (String, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### AuditLog

- Client handle: `c.AuditLog`
- Query namespace: `query.AuditLog`
- Fields:
  - `Id` (String, id)
  - `Actorid` (String, optional)
  - `Actor` (String)
  - `Sourceip` (String)
  - `Useragent` (String)
  - `Action` (String)
  - `Resourcetype` (String)
  - `Resourceid` (String)
  - `Beforejson` (Json, optional)
  - `Afterjson` (Json, optional)
  - `Requestid` (String)
  - `Result` (String)
  - `Failurereason` (String, optional)
  - `Createdat` (DateTime)

### Certificate

- Client handle: `c.Certificate`
- Query namespace: `query.Certificate`
- Fields:
  - `Id` (String, id)
  - `Clusterid` (String)
  - `Name` (String)
  - `Source` ()
  - `Status` ()
  - `Certpem` (String, optional)
  - `Privatekeypem` (String, optional)
  - `Privatekeyencrypted` (String, optional)
  - `Fingerprint` (String, optional)
  - `Serialnumber` (String, optional)
  - `Domainsjson` (Json)
  - `Notbefore` (DateTime, optional)
  - `Expiresat` (DateTime, optional)
  - `Issuer` (String, optional)
  - `Keyalgorithm` (String, optional)
  - `Acmedirectoryurl` (String, optional)
  - `Acmeemail` (String, optional)
  - `Acmechallengetype` (, optional)
  - `Autorenew` (Boolean)
  - `Renewbeforedays` (Int)
  - `Lastissuedat` (DateTime, optional)
  - `Lastrenewalattemptat` (DateTime, optional)
  - `Lastrenewalerror` (String, optional)
  - `Lastpublishedat` (DateTime, optional)
  - `Lastpublisherror` (String, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### CertificateJob

- Client handle: `c.CertificateJob`
- Query namespace: `query.CertificateJob`
- Fields:
  - `Id` (String, id)
  - `Certificateid` (String)
  - `Operation` ()
  - `Status` ()
  - `Attempts` (Int)
  - `Maxattempts` (Int)
  - `Nextattemptat` (DateTime)
  - `Leaseowner` (String, optional)
  - `Leaseuntil` (DateTime, optional)
  - `Heartbeatat` (DateTime, optional)
  - `Idempotencykey` (String, optional, unique)
  - `Cancelrequestedat` (DateTime, optional)
  - `Timeoutat` (DateTime, optional)
  - `Resultjson` (Json, optional)
  - `Compensationjson` (Json, optional)
  - `Error` (String, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### Cluster

- Client handle: `c.Cluster`
- Query namespace: `query.Cluster`
- Fields:
  - `Id` (String, id)
  - `Creatorid` (String)
  - `Name` (String)
  - `Primaryhostname` (String, optional, unique)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### ClusterGroup

- Client handle: `c.ClusterGroup`
- Query namespace: `query.ClusterGroup`
- Fields:
  - `Id` (String, id)
  - `Clusterid` (String)
  - `Name` (String)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### ClusterMember

- Client handle: `c.ClusterMember`
- Query namespace: `query.ClusterMember`
- Fields:
  - `Clusterid` (String)
  - `Userid` (String)
  - `Permission` ()
  - `Createdat` (DateTime)

### ClusterRegion

- Client handle: `c.ClusterRegion`
- Query namespace: `query.ClusterRegion`
- Fields:
  - `Id` (String, id)
  - `Clusterid` (String)
  - `Name` (String)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### ConfigVersion

- Client handle: `c.ConfigVersion`
- Query namespace: `query.ConfigVersion`
- Fields:
  - `Id` (String, id)
  - `Siteid` (String)
  - `Version` (BigInt)
  - `Configjson` (Json)
  - `Hash` (String)
  - `Status` ()
  - `Createdat` (DateTime)

### DNSLine

- Client handle: `c.DNSLine`
- Query namespace: `query.DNSLine`
- Fields:
  - `Id` (String, id)
  - `Clusterid` (String)
  - `Name` (String)
  - `Providercode` (String)
  - `Providerparentcode` (String, optional)
  - `Sortorder` (Int)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### DNSManagedRecord

- Client handle: `c.DNSManagedRecord`
- Query namespace: `query.DNSManagedRecord`
- Fields:
  - `Id` (String, id)
  - `Clusterid` (String)
  - `Sitedomainid` (String, optional)
  - `Dnslineid` (String, optional)
  - `Dnslinekey` (String)
  - `Nodeid` (UUID, optional)
  - `Hostname` (String)
  - `Type` ()
  - `Value` (String)
  - `Providerrecordid` (String, optional)
  - `Status` ()
  - `Lasterror` (String, optional)
  - `Lastsyncedat` (DateTime, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### DNSProviderConfig

- Client handle: `c.DNSProviderConfig`
- Query namespace: `query.DNSProviderConfig`
- Fields:
  - `Id` (String, id)
  - `Clusterid` (String, unique)
  - `Provider` ()
  - `Zone` (String)
  - `Zoneid` (String, optional)
  - `Credentialsencrypted` (String)
  - `Defaultttl` (Int)
  - `Proxied` (Boolean)
  - `Enabled` (Boolean)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### DNSSyncJob

- Client handle: `c.DNSSyncJob`
- Query namespace: `query.DNSSyncJob`
- Fields:
  - `Id` (String, id)
  - `Clusterid` (String)
  - `Siteid` (String, optional)
  - `Action` ()
  - `Status` ()
  - `Attempts` (Int)
  - `Maxattempts` (Int)
  - `Nextattemptat` (DateTime)
  - `Leaseowner` (String, optional)
  - `Leaseuntil` (DateTime, optional)
  - `Heartbeatat` (DateTime, optional)
  - `Idempotencykey` (String, optional, unique)
  - `Cancelrequestedat` (DateTime, optional)
  - `Timeoutat` (DateTime, optional)
  - `Resultjson` (Json, optional)
  - `Compensationjson` (Json, optional)
  - `Error` (String, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### DynamicSetting

- Client handle: `c.DynamicSetting`
- Query namespace: `query.DynamicSetting`
- Fields:
  - `Key` (String, id)
  - `Valuejson` (Json)
  - `Description` (String, optional)
  - `Updatedat` (DateTime)

### InstallJob

- Client handle: `c.InstallJob`
- Query namespace: `query.InstallJob`
- Fields:
  - `Id` (String, id)
  - `Nodeid` (UUID)
  - `Payload` (Json)
  - `Status` ()
  - `Attempts` (Int)
  - `Maxattempts` (Int)
  - `Nextattemptat` (DateTime)
  - `Leaseowner` (String, optional)
  - `Leaseuntil` (DateTime, optional)
  - `Heartbeatat` (DateTime, optional)
  - `Idempotencykey` (String, optional, unique)
  - `Cancelrequestedat` (DateTime, optional)
  - `Timeoutat` (DateTime, optional)
  - `Resultjson` (Json, optional)
  - `Compensationjson` (Json, optional)
  - `Error` (String, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### JobExecution

- Client handle: `c.JobExecution`
- Query namespace: `query.JobExecution`
- Fields:
  - `Id` (String, id)
  - `Jobtype` (String)
  - `Jobid` (String)
  - `Attempt` (Int)
  - `Workerid` (String)
  - `Status` ()
  - `Startedat` (DateTime)
  - `Heartbeatat` (DateTime)
  - `Finishedat` (DateTime, optional)
  - `Resultjson` (Json, optional)
  - `Error` (String, optional)

### Node

- Client handle: `c.Node`
- Query namespace: `query.Node`
- Fields:
  - `Id` (UUID, id)
  - `Clusterid` (String)
  - `Groupid` (String, optional)
  - `Regionid` (String, optional)
  - `Name` (String)
  - `Version` (String, optional)
  - `Heartbeatat` (DateTime, optional)
  - `Status` ()
  - `Installerror` (String, optional)
  - `Sshcredentialid` (UUID, optional)
  - `Sshhost` (String, optional)
  - `Sshport` (Int, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### NodeAddress

- Client handle: `c.NodeAddress`
- Query namespace: `query.NodeAddress`
- Fields:
  - `Id` (String, id)
  - `Nodeid` (UUID)
  - `Address` (String, unique)
  - `Createdat` (DateTime)

### NodeCacheConfig

- Client handle: `c.NodeCacheConfig`
- Query namespace: `query.NodeCacheConfig`
- Fields:
  - `Id` (String, id)
  - `Nodeid` (UUID, unique)
  - `Cachedir` (String)
  - `Automaxsize` (Boolean)
  - `Maxsizebytes` (BigInt, optional)
  - `Maxdiskusagepercent` (Int)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### NodeCredential

- Client handle: `c.NodeCredential`
- Query namespace: `query.NodeCredential`
- Fields:
  - `Nodeid` (UUID, id)
  - `Certificateserial` (String, optional, unique)
  - `Certificatenotafter` (DateTime, optional)
  - `Bootstrapidentityencrypted` (String, optional)
  - `Previouscertificateserial` (String, optional)
  - `Previouscertificatevaliduntil` (DateTime, optional)
  - `Rotationcsrsha256` (String, optional)
  - `Rotationcertificatepem` (String, optional)
  - `Revokedat` (DateTime, optional)

### NodeDNSLine

- Client handle: `c.NodeDNSLine`
- Query namespace: `query.NodeDNSLine`
- Fields:
  - `Nodeid` (UUID)
  - `Dnslineid` (String)

### NodeGroupMembership

- Client handle: `c.NodeGroupMembership`
- Query namespace: `query.NodeGroupMembership`
- Fields:
  - `Nodeid` (UUID)
  - `Groupid` (String)

### NodeHardwareProfile

- Client handle: `c.NodeHardwareProfile`
- Query namespace: `query.NodeHardwareProfile`
- Fields:
  - `Nodeid` (UUID, id)
  - `Architecture` (String)
  - `Cpumodel` (String)
  - `Cachediskwritebytespersecond` (BigInt, optional)
  - `Benchmarkbytes` (BigInt, optional)
  - `Benchmarkdurationms` (Int, optional)
  - `Measuredat` (DateTime)

### NodeRegionMembership

- Client handle: `c.NodeRegionMembership`
- Query namespace: `query.NodeRegionMembership`
- Fields:
  - `Nodeid` (UUID)
  - `Regionid` (String)

### NodeSiteConfigVersion

- Client handle: `c.NodeSiteConfigVersion`
- Query namespace: `query.NodeSiteConfigVersion`
- Fields:
  - `Nodeid` (UUID)
  - `Siteid` (String)
  - `Version` (BigInt)
  - `Status` ()
  - `Updatedat` (DateTime)

### OriginBackend

- Client handle: `c.OriginBackend`
- Query namespace: `query.OriginBackend`
- Fields:
  - `Id` (String, id)
  - `Originpoolid` (String)
  - `Protocol` ()
  - `Address` (String)
  - `Hostheader` (String, optional)
  - `Weight` (Int)
  - `Priority` (Int)
  - `Enabled` (Boolean)
  - `Createdat` (DateTime)

### OriginPool

- Client handle: `c.OriginPool`
- Query namespace: `query.OriginPool`
- Fields:
  - `Id` (String, id)
  - `Clusterid` (String)
  - `Name` (String)
  - `Scheduler` (String)
  - `Timeout` (Int)
  - `Headers` (Json)
  - `Governance` (Json)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### PasswordResetToken

- Client handle: `c.PasswordResetToken`
- Query namespace: `query.PasswordResetToken`
- Fields:
  - `Id` (String, id)
  - `Userid` (String)
  - `Tokenhash` (String, unique)
  - `Expiresat` (DateTime)
  - `Usedat` (DateTime, optional)
  - `Createdat` (DateTime)

### Policy

- Client handle: `c.Policy`
- Query namespace: `query.Policy`
- Fields:
  - `Id` (String, id)
  - `Name` (String)
  - `Cachejson` (Json)
  - `Compressionjson` (Json)
  - `Deliveryjson` (Json)
  - `Wafjson` (Json)
  - `Ccjson` (Json)
  - `Accessjson` (Json)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### PublishJob

- Client handle: `c.PublishJob`
- Query namespace: `query.PublishJob`
- Fields:
  - `Id` (String, id)
  - `Siteid` (String)
  - `Version` (BigInt)
  - `Targets` (Json)
  - `Status` ()
  - `Attempts` (Int)
  - `Maxattempts` (Int)
  - `Nextattemptat` (DateTime)
  - `Leaseowner` (String, optional)
  - `Leaseuntil` (DateTime, optional)
  - `Heartbeatat` (DateTime, optional)
  - `Idempotencykey` (String, optional, unique)
  - `Cancelrequestedat` (DateTime, optional)
  - `Timeoutat` (DateTime, optional)
  - `Resultjson` (Json, optional)
  - `Compensationjson` (Json, optional)
  - `Error` (String, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### PurgeJob

- Client handle: `c.PurgeJob`
- Query namespace: `query.PurgeJob`
- Fields:
  - `Id` (String, id)
  - `Siteid` (String)
  - `Type` ()
  - `Value` (String, optional)
  - `Status` ()
  - `Attempts` (Int)
  - `Maxattempts` (Int)
  - `Nextattemptat` (DateTime)
  - `Leaseowner` (String, optional)
  - `Leaseuntil` (DateTime, optional)
  - `Heartbeatat` (DateTime, optional)
  - `Idempotencykey` (String, optional, unique)
  - `Cancelrequestedat` (DateTime, optional)
  - `Timeoutat` (DateTime, optional)
  - `Resultjson` (Json, optional)
  - `Compensationjson` (Json, optional)
  - `Error` (String, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### SSHCredential

- Client handle: `c.SSHCredential`
- Query namespace: `query.SSHCredential`
- Fields:
  - `Id` (UUID, id)
  - `Clusterid` (String)
  - `Name` (String)
  - `Username` (String)
  - `Authtype` ()
  - `Secretencrypted` (String)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### Site

- Client handle: `c.Site`
- Query namespace: `query.Site`
- Fields:
  - `Id` (String, id)
  - `Clusterid` (String)
  - `Creatorid` (String)
  - `Name` (String)
  - `Status` ()
  - `Originpoolid` (String)
  - `Policyid` (String, optional)
  - `Version` (BigInt)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### SiteCertificate

- Client handle: `c.SiteCertificate`
- Query namespace: `query.SiteCertificate`
- Fields:
  - `Siteid` (String)
  - `Certificateid` (String)

### SiteDomain

- Client handle: `c.SiteDomain`
- Query namespace: `query.SiteDomain`
- Fields:
  - `Id` (String, id)
  - `Siteid` (String)
  - `Hostname` (String, unique)
  - `Createdat` (DateTime)

### SiteListenerConfig

- Client handle: `c.SiteListenerConfig`
- Query namespace: `query.SiteListenerConfig`
- Fields:
  - `Id` (String, id)
  - `Siteid` (String, unique)
  - `Httpenabled` (Boolean)
  - `Httpport` (Int)
  - `Redirecthttptohttps` (Boolean)
  - `Httpsenabled` (Boolean)
  - `Httpsport` (Int)
  - `Http2enabled` (Boolean)
  - `Http3enabled` (Boolean)
  - `Tlsminversion` ()
  - `Hstsenabled` (Boolean)
  - `Hstsmaxage` (Int)
  - `Hstsincludesubdomains` (Boolean)
  - `Hstspreload` (Boolean)
  - `Ocspstaplingenabled` (Boolean)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### SiteTemplate

- Client handle: `c.SiteTemplate`
- Query namespace: `query.SiteTemplate`
- Fields:
  - `Id` (String, id)
  - `Clusterid` (String)
  - `Name` (String)
  - `Configjson` (Json)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### User

- Client handle: `c.User`
- Query namespace: `query.User`
- Fields:
  - `Id` (String, id)
  - `Email` (String, unique)
  - `Passwordhash` (String)
  - `Name` (String)
  - `Role` ()
  - `Status` ()
  - `Totpsecret` (String, optional)
  - `Totprecoverycodes` (Json, optional)
  - `Failedloginattempts` (Int)
  - `Lastfailedloginat` (DateTime, optional)
  - `Lockeduntil` (DateTime, optional)
  - `Lastloginat` (DateTime, optional)
  - `Passwordchangedat` (DateTime, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### UserSession

- Client handle: `c.UserSession`
- Query namespace: `query.UserSession`
- Fields:
  - `Id` (String, id)
  - `Userid` (String)
  - `Tokenhash` (String, unique)
  - `Ipaddress` (String, optional)
  - `Useragent` (String, optional)
  - `Expiresat` (DateTime)
  - `Lastseenat` (DateTime)
  - `Revokedat` (DateTime, optional)
  - `Createdat` (DateTime)

### When generating application code:

1. Prefer staged builders (`Query/Create/Update/Delete`) over one-shot calls.
2. Use `query.<Model>.<Field>` helpers for all conditions and set operations.
3. For optional fields, use `Set(value)` for non-null values and `SetNull()` to write NULL.
4. Use `DoMany` only when multiple-row side effects are intended.
5. Preserve explicit error handling on every DB operation.
