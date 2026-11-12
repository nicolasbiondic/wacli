package wa_test

import (
	"context"

	waStore "go.mau.fi/whatsmeow/store"
)

// Preserve enumeration through the transport's instrumentation wrapper. Full
// sync now refuses destructive resets when a verified backup cannot be read.
func (s transportApplyStore) GetAllAppStateMutationMACs(ctx context.Context, name string) ([]waStore.AppStateMutationMAC, error) {
	return s.AppStateStore.(interface {
		GetAllAppStateMutationMACs(context.Context, string) ([]waStore.AppStateMutationMAC, error)
	}).GetAllAppStateMutationMACs(ctx, name)
}
