//go:build cgo

package sqliteutil

import "github.com/mattn/go-sqlite3"

func DescribeError(err error) *ErrorDetails {
	return describeError(err, false)
}

func describeError(err error, readOnly bool) *ErrorDetails {
	var sqliteErr sqlite3.Error
	switch e := err.(type) {
	case *readOnlyError:
		return describeError(e.error, true)
	case sqlite3.Error:
		sqliteErr = e
	case *sqlite3.Error:
		if e == nil {
			return nil
		}
		sqliteErr = *e
	case sqlite3.ErrNo:
		sqliteErr.Code = e
	case sqlite3.ErrNoExtended:
		sqliteErr.Code, sqliteErr.ExtendedCode = sqlite3.ErrNo(int(e)&255), e
	case interface{ Unwrap() error }:
		return describeError(e.Unwrap(), readOnly)
	case interface{ Unwrap() []error }:
		for _, cause := range e.Unwrap() {
			if details := describeError(cause, readOnly); details != nil {
				return details
			}
		}
		return nil
	default:
		return nil
	}
	primary, extended := int(sqliteErr.Code), int(sqliteErr.ExtendedCode)
	if extended == 0 {
		extended = primary
	}
	return &ErrorDetails{
		PrimaryCode: primary, ExtendedCode: extended, Name: sqliteErrorName(primary, extended),
		Retryable: readOnly && primary == 8 && (extended == 264 || extended == 1288),
	}
}

// Names are SQLite result-code identifiers, not the driver's error message.
// Unknown extensions retain their exact number and never get a guessed name.
func sqliteErrorName(primary, extended int) string {
	primaryNames := [...]string{"SQLITE_OK", "SQLITE_ERROR", "SQLITE_INTERNAL", "SQLITE_PERM", "SQLITE_ABORT", "SQLITE_BUSY", "SQLITE_LOCKED", "SQLITE_NOMEM", "SQLITE_READONLY", "SQLITE_INTERRUPT", "SQLITE_IOERR", "SQLITE_CORRUPT", "SQLITE_NOTFOUND", "SQLITE_FULL", "SQLITE_CANTOPEN", "SQLITE_PROTOCOL", "SQLITE_EMPTY", "SQLITE_SCHEMA", "SQLITE_TOOBIG", "SQLITE_CONSTRAINT", "SQLITE_MISMATCH", "SQLITE_MISUSE", "SQLITE_NOLFS", "SQLITE_AUTH", "SQLITE_FORMAT", "SQLITE_RANGE", "SQLITE_NOTADB", "SQLITE_NOTICE", "SQLITE_WARNING"}
	if extended == primary && primary >= 0 && primary < len(primaryNames) {
		return primaryNames[primary]
	}
	extensions := map[int][]string{
		1:  {"", "SQLITE_ERROR_MISSING_COLLSEQ", "SQLITE_ERROR_RETRY", "SQLITE_ERROR_SNAPSHOT"},
		4:  {"", "", "SQLITE_ABORT_ROLLBACK"},
		5:  {"", "SQLITE_BUSY_RECOVERY", "SQLITE_BUSY_SNAPSHOT", "SQLITE_BUSY_TIMEOUT"},
		6:  {"", "SQLITE_LOCKED_SHAREDCACHE", "SQLITE_LOCKED_VTAB"},
		8:  {"", "SQLITE_READONLY_RECOVERY", "SQLITE_READONLY_CANTLOCK", "SQLITE_READONLY_ROLLBACK", "SQLITE_READONLY_DBMOVED", "SQLITE_READONLY_CANTINIT", "SQLITE_READONLY_DIRECTORY"},
		10: {"", "SQLITE_IOERR_READ", "SQLITE_IOERR_SHORT_READ", "SQLITE_IOERR_WRITE", "SQLITE_IOERR_FSYNC", "SQLITE_IOERR_DIR_FSYNC", "SQLITE_IOERR_TRUNCATE", "SQLITE_IOERR_FSTAT", "SQLITE_IOERR_UNLOCK", "SQLITE_IOERR_RDLOCK", "SQLITE_IOERR_DELETE", "SQLITE_IOERR_BLOCKED", "SQLITE_IOERR_NOMEM", "SQLITE_IOERR_ACCESS", "SQLITE_IOERR_CHECKRESERVEDLOCK", "SQLITE_IOERR_LOCK", "SQLITE_IOERR_CLOSE", "SQLITE_IOERR_DIR_CLOSE", "SQLITE_IOERR_SHMOPEN", "SQLITE_IOERR_SHMSIZE", "SQLITE_IOERR_SHMLOCK", "SQLITE_IOERR_SHMMAP", "SQLITE_IOERR_SEEK", "SQLITE_IOERR_DELETE_NOENT", "SQLITE_IOERR_MMAP", "SQLITE_IOERR_GETTEMPPATH", "SQLITE_IOERR_CONVPATH", "SQLITE_IOERR_VNODE", "SQLITE_IOERR_AUTH", "SQLITE_IOERR_BEGIN_ATOMIC", "SQLITE_IOERR_COMMIT_ATOMIC", "SQLITE_IOERR_ROLLBACK_ATOMIC", "SQLITE_IOERR_DATA", "SQLITE_IOERR_CORRUPTFS"},
		11: {"", "SQLITE_CORRUPT_VTAB", "SQLITE_CORRUPT_SEQUENCE", "SQLITE_CORRUPT_INDEX"},
		14: {"", "SQLITE_CANTOPEN_NOTEMPDIR", "SQLITE_CANTOPEN_ISDIR", "SQLITE_CANTOPEN_FULLPATH", "SQLITE_CANTOPEN_CONVPATH", "SQLITE_CANTOPEN_DIRTYWAL", "SQLITE_CANTOPEN_SYMLINK"},
		19: {"", "SQLITE_CONSTRAINT_CHECK", "SQLITE_CONSTRAINT_COMMITHOOK", "SQLITE_CONSTRAINT_FOREIGNKEY", "SQLITE_CONSTRAINT_FUNCTION", "SQLITE_CONSTRAINT_NOTNULL", "SQLITE_CONSTRAINT_PRIMARYKEY", "SQLITE_CONSTRAINT_TRIGGER", "SQLITE_CONSTRAINT_UNIQUE", "SQLITE_CONSTRAINT_VTAB", "SQLITE_CONSTRAINT_ROWID", "SQLITE_CONSTRAINT_PINNED", "SQLITE_CONSTRAINT_DATATYPE"},
		23: {"", "SQLITE_AUTH_USER"},
		27: {"", "SQLITE_NOTICE_RECOVER_WAL", "SQLITE_NOTICE_RECOVER_ROLLBACK", "SQLITE_NOTICE_RBU"},
		28: {"", "SQLITE_WARNING_AUTOINDEX"},
	}
	index := extended >> 8
	if names := extensions[primary]; extended&255 == primary && index > 0 && index < len(names) && names[index] != "" {
		return names[index]
	}
	return "SQLITE_UNKNOWN"
}
