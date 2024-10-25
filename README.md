# tmsqlite

tmsqlite provides an [SQLite](https://www.sqlite.org/)-backed storage layer
for [Gordian](https://github.com/gordian-engine/gordian)'s
[tmstore](https://pkg.go.dev/github.com/gordian-engine/gordian/tm/tmstore) package.

This module defaults to using [github.com/mattn/go-sqlite3/](https://github.com/mattn/go-sqlite3/)
for a [CGo](https://pkg.go.dev/cmd/cgo)-backed SQLite driver.
If CGo is unavailable, it falls back to the [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) driver,
which is a pure Go implementation of SQLite.

If CGo is available and you still prefer to run the pure Go driver,
use the `purego` build tag, e.g. `go test -tags=purego ./...`.

## Project status

tmsqlite passes the [tmstore compliance tests](https://pkg.go.dev/github.com/gordian-engine/gordian/tm/tmstore/tmstoretest)
and it runs with [gcosmos](https://github.com/gordian-engine/gcosmos),
but until there is a tagged release, you should assume that the database schema may change in a backwards-incompatible way.

## License

The tmsqlite source code is available under the Apache 2.0 license.

Copyright (c) 2024 Strangelove Crypto, Inc.
