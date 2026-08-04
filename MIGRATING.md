# Migrating to `github.com/warehouse-pg/common-go-libs`

## Why the path changed

The `warehouse-pg` fork still declared `module github.com/greenplum-db/gp-common-go-libs`,
so Go refused to resolve it by its real location:

```
github.com/warehouse-pg/gp-common-go-libs/dbconn: github.com/warehouse-pg/gp-common-go-libs@v1.0.16:
	parsing go.mod:
	module declares its path as: github.com/greenplum-db/gp-common-go-libs
```

The workaround was a `replace` directive in every consumer, which also blocked
upgrading pgx v4 → v5. The module path is now `github.com/warehouse-pg/common-go-libs`,
which removes both problems.

## Nothing breaks if you do nothing

Go resolves a module path against the tags published under that path. The old
`v1.0.x` tags remain reachable, so an unmodified consumer keeps building. There
is no need for any repo to support both paths at once, and no deadline to
migrate.

Long-lived maintenance branches can stay on the old path indefinitely; migrate
`main` first and do not backport the path change.

## Migrating

1. Update `go.mod`:

   ```
   go mod edit -dropreplace=github.com/greenplum-db/gp-common-go-libs
   go mod edit -droprequire=github.com/greenplum-db/gp-common-go-libs
   go get github.com/warehouse-pg/common-go-libs@v1.1.0
   ```

2. Rewrite imports:

   ```
   grep -rl github.com/greenplum-db/gp-common-go-libs --include='*.go' . \
     | xargs sed -i 's#github.com/greenplum-db/gp-common-go-libs#github.com/warehouse-pg/common-go-libs#g'
   ```

3. Work through the breaking changes below, then `go mod tidy`.

## Breaking changes in v1.1.0

`sqlx` and `github.com/pkg/errors` are gone. Both leaked into exported
signatures, so this is not a drop-in upgrade.

### `sqlx` types are now `database/sql` types

| Before | After |
| --- | --- |
| `DBConn.ConnPool []*sqlx.DB` | `[]*sql.DB` |
| `DBConn.Tx []*sqlx.Tx` | `[]*sql.Tx` |
| `DBDriver.Connect(...) (*sqlx.DB, error)` | `(*sql.DB, error)` |
| `Query`/`QueryWithArgs`/`QueryContext` return `*sqlx.Rows` | `*sql.Rows` |
| `testhelper.CreateMockDB() (*sqlx.DB, ...)` | `(*sql.DB, ...)` |
| `testhelper.TestDriver.DB *sqlx.DB` | `*sql.DB` |

`*sql.Rows` has no `StructScan`. Either scan the columns explicitly, or use
`Get`/`Select`, which do the struct mapping for you:

```go
// before
rows, _ := conn.Query(query)
for rows.Next() {
    var row myStruct
    rows.StructScan(&row)
}

// after
var rows []myStruct
err := conn.Select(&rows, query)
```

`Get` and `Select` map columns to fields via `db:"..."` struct tags, falling back
to the lowercased field name — the same rules sqlx used, so existing structs
generally need no changes. Fields tagged `db:"-"` are skipped, embedded structs
(values or pointers, with nil pointers allocated on demand) are flattened
following Go's own field promotion rules, and any type implementing
`sql.Scanner` is passed through untouched. Structs with no exported fields,
such as `time.Time`, are handed to the driver directly.

A result column with no destination field is an error (`missing destination
field "..."`), the same contract as sqlx's `missing destination name`: a tag
typo or a renamed column fails the query instead of silently producing zero
values.

### `Get` no longer silently takes the first row

`sqlx.Get` returned the first row of a multi-row result. `dbconn.Get` now returns
`dbconn.ErrMultipleRows`. It still returns `sql.ErrNoRows` for an empty result.

`Get` is for queries that are single-row by construction (scalar functions,
`SHOW <guc>`, primary-key catalog lookups). Audit call sites that could
legitimately return several rows and move them to `Select`.

### `pq.StringArray` is replaced by `dbconn.StringArray`

`github.com/lib/pq` is no longer a dependency. `dbconn.StringArray` is a
`sql.Scanner` over the Postgres array text format, matching pq's strict handling
of an unquoted `NULL` element, and it rejects multidimensional array values
with an error as pq did. It deliberately does not implement `driver.Valuer`,
since no caller binds an array as a query parameter.

### Errors are stdlib errors

Nothing returns a `github.com/pkg/errors` error any more, so `errors.Cause` and
`%+v` stack formatting no longer apply. Wrapping uses `fmt.Errorf("...: %w", err)`,
so `errors.Is` and `errors.As` work. `gplog.Fatal` still logs a stack trace,
captured via `runtime.Callers`.

### Behavior changes that keep their signatures

- **Connection errors are classified by SQLSTATE** (`42704` for a missing role,
  `3D000` for a missing database) instead of matching lib/pq's `pq: ` message
  prefixes, which pgx never produces. If you construct connection-failure
  fixtures in tests, build a `*pgconn.PgError` with the right `Code`.
- **`Begin` uses `BeginTx` with `sql.LevelSerializable`** instead of `BEGIN`
  followed by `SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`. Tests that mocked
  both statements should now expect only the begin.
- **`SelectString` and `SelectInt` scan in a single pass** and report more than
  one row as `Too many rows returned from query: expected at most 1 row`. The
  old message named the row count.
- **`cluster.MustGetSegmentConfiguration` forwards its full variadic** to
  `GetSegmentConfiguration`. It previously collapsed the arguments to
  `len(getMirrors) == 1 && getMirrors[0]`, so `(true, true)` — documented as
  "mirrors and standby only" — silently returned primaries instead.
- **`testhelper.MockFileContents` writes from a goroutine**, so contents larger
  than the pipe buffer no longer deadlock.

## API that was intentionally kept

Nothing was removed from the exported API. In particular these are all still
present, even though the `database/sql` rewrite touched or could have dropped
them:

- `dbconn`: `ExecContext`, `MustExecContext`, `GetWithArgs`, `SelectWithArgs`,
  `SelectContext`, `Query`, `QueryWithArgs`, `QueryContext`,
  `ConnectInUtilityMode`, `MustConnectInUtilityMode`, `SelectInt`,
  `MustSelectInt`, `SelectIntSlice`, `MustSelectIntSlice`, `StringToSemVerRange`,
  `GPDBVersion.Is`
- `iohelper`: `OpenFileForAppending`, `MustOpenFileForAppending`
- `gplog`: `SetLogPrefixFunc`, `SetShellLogPrefixFunc`
- `conv` and `gperror` in full
