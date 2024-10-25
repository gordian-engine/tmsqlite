//go:build cgo && !purego

package tmsqlite

import (
	"errors"

	"github.com/mattn/go-sqlite3"
)

const (
	sqliteDriverType = "sqlite3"
	sqliteBuildType  = "cgo"
)

func isPrimaryKeyConstraintError(e error) bool {
	var sErr sqlite3.Error
	if !errors.As(e, &sErr) {
		return false
	}

	return sErr.Code == sqlite3.ErrConstraint && sErr.ExtendedCode == sqlite3.ErrConstraintPrimaryKey
}

func isUniqueConstraintError(e error) bool {
	var sErr sqlite3.Error
	if !errors.As(e, &sErr) {
		return false
	}

	return sErr.Code == sqlite3.ErrConstraint && sErr.ExtendedCode == sqlite3.ErrConstraintUnique
}

func isNotNullConstraintError(e error) bool {
	var sErr sqlite3.Error
	if !errors.As(e, &sErr) {
		return false
	}

	return sErr.Code == sqlite3.ErrConstraint && sErr.ExtendedCode == sqlite3.ErrConstraintNotNull
}
