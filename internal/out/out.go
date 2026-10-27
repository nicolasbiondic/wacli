package out

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/openclaw/wacli/internal/sqliteutil"
)

type envelope struct {
	Success     bool                     `json:"success"`
	Data        any                      `json:"data"`
	Error       *string                  `json:"error"`
	SQLiteError *sqliteutil.ErrorDetails `json:"sqlite_error,omitempty"`
}

func WriteJSON(w io.Writer, data any) error {
	b, err := json.Marshal(envelope{Success: true, Data: data})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	if isPlatformBrokenPipe(err) {
		return nil
	}
	return err
}

func WriteError(w io.Writer, asJSON bool, err error) error {
	if err == nil {
		return nil
	}
	if asJSON {
		msg := err.Error()
		b, _ := json.Marshal(envelope{Success: false, Data: nil, Error: &msg, SQLiteError: sqliteutil.DescribeError(err)})
		_, _ = fmt.Fprintln(w, string(b))
		return nil
	}
	_, _ = fmt.Fprintln(w, SanitizeHuman(err.Error()))
	return nil
}

// ErrorFields keeps lifecycle JSON errors consistent with the CLI envelope.
func ErrorFields(err error) map[string]any {
	fields := map[string]any{"message": err.Error()}
	if details := sqliteutil.DescribeError(err); details != nil {
		fields["sqlite_error"] = details
	}
	return fields
}
