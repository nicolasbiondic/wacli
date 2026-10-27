package sqliteutil

// ErrorDetails describes a SQLite failure without changing its message or cause.
type ErrorDetails struct {
	PrimaryCode  int    `json:"primary_code"`
	ExtendedCode int    `json:"extended_code"`
	Name         string `json:"name"`
	Retryable    bool   `json:"retryable"`
}

type readOnlyError struct{ error }

func (e *readOnlyError) Unwrap() error { return e.error }

// MarkReadOnlyError records provenance, not a request to retry a whole command.
// Only read-only openings and read views should mark their returned failures.
func MarkReadOnlyError(err error) error {
	if err == nil {
		return nil
	}
	return &readOnlyError{err}
}

// IsReadOnlyTransient recognises the two known WAL/SHM read-side init failures.
// Callers must enforce read-only scope and their own bounded retry budget.
func IsReadOnlyTransient(err error) bool {
	details := DescribeError(MarkReadOnlyError(err))
	return details != nil && details.Retryable
}
