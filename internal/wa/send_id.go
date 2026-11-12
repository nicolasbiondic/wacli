package wa

import (
	"context"

	"go.mau.fi/whatsmeow/types"
)

type sendMessageIDKey struct{}

// WithMessageID fixes the ID before dispatch, allowing the caller to register
// per-message policy before an own-device echo can arrive. It affects only this
// send attempt; ordinary sends still let whatsmeow generate their ID.
func WithMessageID(ctx context.Context, id types.MessageID) context.Context {
	return context.WithValue(ctx, sendMessageIDKey{}, id)
}

func MessageIDFromContext(ctx context.Context) types.MessageID {
	id, _ := ctx.Value(sendMessageIDKey{}).(types.MessageID)
	return id
}
