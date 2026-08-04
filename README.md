# common-go-libs

Shared Go packages used across the WarehousePG tooling repos.

```
go get github.com/warehouse-pg/common-go-libs
```

This module was previously published as `github.com/greenplum-db/gp-common-go-libs`.
The fork under `warehouse-pg` kept that old module path in its `go.mod`, so
consumers could only reach it through a `replace` directive; the path is now
`github.com/warehouse-pg/common-go-libs` and the `replace` is no longer needed.
See [MIGRATING.md](MIGRATING.md) for the details, including the breaking API
changes that landed alongside the rename.

## Packages

| Package | Purpose |
| --- | --- |
| `cluster` | Segment topology (`gp_segment_configuration`) plus local and SSH command execution across a cluster |
| `conv` | Allocation-conscious number/byte conversions and MD5 helpers |
| `dbconn` | Connection pool with per-goroutine session affinity, query helpers, and version parsing |
| `gperror` | Error type carrying a numeric error code |
| `gplog` | Leveled logging to both a log file and the console, with independent verbosity for each |
| `iohelper` | File open/read/write helpers routed through `operating` so tests can mock them |
| `operating` | Function pointers for filesystem, shell, and environment calls, for dependency injection |
| `structmatcher` | Gomega matcher that diffs structs field by field, with include/exclude filters |
| `testhelper` | Mock DB connections, mock executors, and log-assertion helpers for consumers' tests |

`dbconn` runs on `database/sql` with the [pgx](https://github.com/jackc/pgx) v5
stdlib driver.

## Development

```
make          # depend + lint + vet + unit
make lint     # golangci-lint run (includes gofmt and goimports)
make fmt      # golangci-lint fmt, rewriting files in place
make unit     # ginkgo, pinned via the go.mod tool directive
make coverage # per-package coverage summary
make tools    # install golangci-lint at the pinned version
```

`make lint` needs `golangci-lint` on `PATH`; everything else runs from the Go
toolchain alone.
