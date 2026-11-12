package main

import (
	"context"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func sendWithUnreadPolicy(ctx context.Context, db *store.DB, keep bool, send func(context.Context) (types.MessageID, error)) (types.MessageID, error) {
	if !keep {
		return send(ctx)
	}
	id := whatsmeow.GenerateMessageID()
	complete := db.RegisterKeepUnreadIntent(ctx, id)
	sentID, err := send(wa.WithMessageID(ctx, id))
	complete(err)
	// Successful persistence consumes the intent. If persistence fails, its
	// bounded lifetime still covers an echo while the command reports a warning.
	return sentID, err
}
