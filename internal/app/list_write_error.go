package app

import (
	"errors"
	"fmt"

	"github.com/openclaw/wacli/internal/store"
)

// Stable codes for `regular` collection writes. Callers must branch on these,
// never on the human message, which can carry list names and JIDs.
const (
	ListErrDuplicateName         = "list_duplicate_name"
	ListErrNotFound              = "list_not_found"
	ListErrDeleted               = "list_deleted"
	ListErrAmbiguousName         = "list_ambiguous_name"
	ListErrIDInUse               = "list_id_in_use"
	ListErrIDInvalid             = "list_id_invalid"
	ListErrImmutable             = "list_immutable"
	ListErrSystemType            = "list_system_type"
	ListErrChatLIDOnly           = "chat_lid_only"
	ListErrIndexIncomplete       = "list_index_incomplete"
	ListErrRegularWritesDisabled = "regular_writes_disabled"
	ListErrCoordinatorDown       = "coordinator_unavailable"
	ListErrWriteOutcomeUnknown   = "write_outcome_unknown"
	ListErrInvalidInput          = "invalid_input"
	ListErrOwnerUnsupported      = "owner_unsupported"
	// ListErrWriteRejected covers a refusal before anything was sent that has
	// no more specific code, for example a local store read failure.
	ListErrWriteRejected = "write_rejected"
	// ListErrDelegateError is what a reply without a code from an older sync
	// owner maps to: the outcome cannot be proven.
	ListErrDelegateError = "delegate_error"
)

const (
	WriteOutcomeNotSent = "not_sent"
	WriteOutcomeUnknown = "unknown"
)

// ListWriteError is a write failure with a stable code and a statement of
// whether the patch may have reached WhatsApp. Error() is the human message.
type ListWriteError struct {
	Code    string
	Outcome string
	Err     error
}

func (e *ListWriteError) Error() string { return e.Err.Error() }
func (e *ListWriteError) Unwrap() error { return e.Err }

// ErrorCode and WriteOutcome let the JSON error envelope read the fields
// without importing this package.
func (e *ListWriteError) ErrorCode() string    { return e.Code }
func (e *ListWriteError) WriteOutcome() string { return e.Outcome }

// NewListWriteError builds a typed error. An empty outcome means not sent.
func NewListWriteError(code, outcome string, err error) *ListWriteError {
	if outcome == "" {
		outcome = WriteOutcomeNotSent
	}
	return &ListWriteError{Code: code, Outcome: outcome, Err: err}
}

func notSent(code string, format string, args ...any) error {
	return NewListWriteError(code, WriteOutcomeNotSent, fmt.Errorf(format, args...))
}

// ensureListWriteError keeps an already typed error and otherwise tags it as a
// refusal before send. Lookup errors from the store keep their meaning.
func ensureListWriteError(err error) error {
	if err == nil {
		return nil
	}
	var typed *ListWriteError
	if errors.As(err, &typed) {
		return err
	}
	code := ListErrWriteRejected
	switch {
	case errors.Is(err, store.ErrListDeleted):
		code = ListErrDeleted
	case errors.Is(err, store.ErrListNotFound):
		code = ListErrNotFound
	case errors.Is(err, store.ErrListAmbiguous):
		code = ListErrAmbiguousName
	}
	return NewListWriteError(code, WriteOutcomeNotSent, err)
}

// unknownOutcome marks a failure at or after the send: the patch may have been
// applied.
func unknownOutcome(err error) error {
	if err == nil {
		return nil
	}
	var typed *ListWriteError
	if errors.As(err, &typed) {
		return err
	}
	return NewListWriteError(ListErrWriteOutcomeUnknown, WriteOutcomeUnknown, err)
}
