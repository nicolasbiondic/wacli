//go:build !cgo

package store

// The driver's non-CGO stub has no SQLite error types or working connections.
// Keep the store cross-compilable and the CLI's explicit CGO guard reachable.
func isReadonlyRecovery(error) bool { return false }
