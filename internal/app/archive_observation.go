package app

import (
	"context"
	"maps"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Carry dispatch order across the persistence queue. These are ordering hints,
// not preferences: only successfully persisted settings enable an unarchive.
func (a *App) observeArchiveEvent(ctx context.Context, evt any, persisted bool) {
	var jid string
	var ms int64
	setting := false
	switch v := evt.(type) {
	case *events.Archive:
		if v == nil || v.Action == nil || v.Timestamp.IsZero() || v.JID.IsEmpty() {
			return
		}
		// Keep the event identity stable until this exact event is persisted.
		// Its phone mapping can appear while it waits in the queue.
		jid = canonicalJIDString(v.JID.ToNonAD())
		ms = v.Timestamp.UnixMilli()
	case *events.UnarchiveChatsSetting:
		if v == nil || v.Action == nil || v.Action.UnarchiveChats == nil || v.Timestamp.IsZero() {
			return
		}
		setting = true
		ms = v.Timestamp.UnixMilli()
	default:
		return
	}
	a.archiveObservationMu.Lock()
	defer a.archiveObservationMu.Unlock()
	if setting {
		if persisted {
			if a.archiveSettingObserved == ms {
				a.archiveSettingObserved = 0
			}
		} else {
			a.archiveSettingObserved = max(a.archiveSettingObserved, ms)
		}
		return
	}
	if persisted {
		if a.archiveObserved[jid] == ms {
			delete(a.archiveObserved, jid)
		}
		return
	}
	if a.archiveObserved == nil {
		a.archiveObserved = make(map[string]int64)
	}
	a.archiveObserved[jid] = max(a.archiveObserved[jid], ms)
}

func (a *App) observedArchiveBoundary(ctx context.Context, chat types.JID) (int64, int64) {
	jid := canonicalJIDString(a.canonicalStoreJID(ctx, chat))
	a.archiveObservationMu.Lock()
	observed := maps.Clone(a.archiveObserved)
	setting := a.archiveSettingObserved
	a.archiveObservationMu.Unlock()
	boundary := setting
	for raw, ms := range observed {
		if raw == jid || a.canonicalStoreJIDString(ctx, raw) == jid {
			boundary = max(boundary, ms)
		}
	}
	return boundary, setting
}
