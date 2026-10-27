//go:build !cgo

package sqliteutil

// The driver's non-CGO stub has no SQLite error types or working connections.
// Keep the store cross-compilable and the CLI's explicit CGO guard reachable.
func DescribeError(error) *ErrorDetails { return nil }
