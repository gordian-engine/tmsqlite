//go:build purego || !cgo

package tmsqlite

import (
	"errors"

	"modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

const (
	sqliteDriverType = "sqlite"
	sqliteBuildType  = "purego"
)

func isPrimaryKeyConstraintError(e error) bool {
	var sErr *sqlite.Error
	if !errors.As(e, &sErr) {
		return false
	}

	// The mattn/go-sqlite3 library separates the plain and extended error codes,
	// but the pure Go version only exposes the extended code, under the "Code()" function.
	// It's probably less precise but it works for what we need here.
	return sErr.Code() == sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY
}

func isUniqueConstraintError(e error) bool {
	var sErr *sqlite.Error
	if !errors.As(e, &sErr) {
		return false
	}

	return sErr.Code() == sqlitelib.SQLITE_CONSTRAINT_UNIQUE
}

func isNotNullConstraintError(e error) bool {
	var sErr *sqlite.Error
	if !errors.As(e, &sErr) {
		return false
	}

	return sErr.Code() == sqlitelib.SQLITE_CONSTRAINT_NOTNULL
}
