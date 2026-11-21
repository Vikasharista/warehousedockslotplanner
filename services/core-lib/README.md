# core-lib

The shared Go library for SetuHaul's Go services. Services scaffolded with the devkit bootstrap skill
(`devkit:bootstrap`) import it as their `core_module`, and it carries every package and symbol those templates call.
So the bootstrap output builds and passes its tests without edits (see [Golden check](#golden-check)).

Module path: `github.com/Vikasharista/warehousedockslotplanner/services/core-lib`. Go 1.24.7 or later.

## Packages

| Package | What it gives a service |
| --- | --- |
| `cmd` | The command line: `serve`, `migrate up [n]`, `migrate down [n]` (golang-migrate, `migrations/`). `down` with no count rolls back one step, never everything |
| `config` | YAML config in a shared Viper: base file, optional override file, then `APP_*` environment variables (`database.postgres.dsn` → `APP_DATABASE_POSTGRES_DSN`) |
| `logger` | The process-wide zap logger. It discards output until `Init` |
| `database/postgres` | The shared GORM pool: `InitPostgres` pings before it swaps the pool in |
| `transaction` | `Executor.DoInTransaction` runs repository work in one transaction carried on the context; nested calls join it. Repositories embed `*GormExecutor` for `GetDB(ctx)` |
| `model` | Base models (int or UUID id, optionally versioned) with namespace, `is_active`, audit users, timestamps and soft delete. Hooks fill namespace and users from the request; a write with no namespace or user is refused. `Map` is a JSONB column |
| `errors` | Coded errors (`BAD_REQUEST`, `RECORD_NOT_FOUND`, `UNAUTHENTICATED`, `FORBIDDEN`, ...). The codes are untyped strings so they key `map[string]int`. `RepositoryError` maps Postgres errors and never exposes the driver's message |
| `utility` | Namespace and user on the context (`WithNamespace`, `GetNamespaceID`, ...); `AppendNamespaceFilterToQuery`, which fails rather than read across namespaces |
| `dto` | The list request: filters, sorts, paging; `ToRQP` converts it for the repository |
| `validator` | go-playground validation that names JSON fields. `nonzero` means "present" (a non-nil pointer) |
| `graphql` | `EnrichContext` (headers → context) and `PresentGqlError`: known errors keep their code and message; anything else is logged and returned as a bare `INTERNAL_SERVER_ERROR`, panics included |
| `graphql/loader` | Generic batched dataloaders over a `GetByIDs`-style fetch. A missing key gets `RECORD_NOT_FOUND` |
| `graphql/authdirective` | `@authenticate` and `@hasPermission`, chosen by `auth.provider` (below), plus `VerifierDirective` for services behind an authenticating gateway |

## Authentication

`authdirective.New(authdirective.ConfigFromViper(config.Viper, "auth"))` builds the directive. It makes no network
calls at start-up.

**`jwt`**: verifies an HS256 bearer token.

| Claim | Meaning |
| --- | --- |
| `sub` | User id. It replaces any `X-User-Id` header the caller sent |
| `permissions` | Permission strings granted; `"*"` grants all |
| `namespaces` | Optional. When present, `X-Namespace-Id` must be one of them |
| `exp` | Required |
| `iss`, `aud` | Checked when `auth.jwt.issuer` / `auth.jwt.audience` are set |

Put the secret in `APP_AUTH_JWT_SECRET`, never in a config file. Tokens signed with any other algorithm, including
`none`, are refused.

**`permsvc`**: asks two HTTP services. The caller's `Authorization` header is forwarded.

```text
POST <auth.identity.url>/v1/sessions/verify                      -> 200 {"userId", "permissions"?, "namespaces"?}
POST <auth.permsvc.url>/v1/permissions/check {userId, namespace, permission} -> 200 {"allowed": bool}
```

Refused callers get `UNAUTHENTICATED` or `FORBIDDEN`. An unreachable or failing auth service is an uncoded error, so
the transports answer 503 rather than blaming the caller. `auth.http.timeout` defaults to 3 s. `auth.cache.*` is
accepted but not used yet: every request is verified.

`X-Namespace-Id` and `X-User-Id` are trusted as sent, so run services behind a gateway that sets them. The JWT
provider is the exception: the token's subject wins.

## Tests

```bash
go test ./...
```

Database tests (model hooks, transactions, pool, migrations) need Postgres and skip without it:

```bash
TEST_DATABASE_DSN="host=localhost port=55432 user=postgres dbname=postgres sslmode=disable" go test -cover ./...
```

Each test works in its own schema, which it drops at the end.

## Golden check

Renders the devkit's `widget-service` example against this checkout, then runs gqlgen, build, vet, the 100%
coverage gate and the e2e suite:

```bash
services/core-lib/scripts/golden.sh ~/developmentkit postgres://postgres@localhost:55432
```

Known limitation: `deleted_by` is not filled on soft delete. Modules that need it set it before `Delete`.
