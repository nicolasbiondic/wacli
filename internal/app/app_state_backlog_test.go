package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
)

// The SDK refuses a fetch while the collection's committed events are still
// backed up in handlers. Nothing was fetched or stored, so the refusal must
// not cost coverage, create replay debt or mark the collection broken. The
// command is retried, and the operator sees how deep the backlog is.
func TestAppStateBacklogIsTransient(t *testing.T) {
	a, f := newLabelWriteApp(t)
	var eventLog bytes.Buffer
	a.opts.Events = out.NewEventWriter(&eventLog, true)
	if err := a.db.MarkRegularFullSyncComplete(); err != nil {
		t.Fatal(err)
	}
	f.appStateFetchErr = fmt.Errorf("fetch: %w", &whatsmeow.AppStateBacklogError{
		Name:            appstate.WAPatchRegular,
		AppStateBacklog: whatsmeow.AppStateBacklog{Depth: 64, OldestAge: 90 * time.Second},
	})

	fetchErr, persistErr := a.fetchAndPersistAppState(t.Context(), appstate.WAPatchRegular, true, &appStatePersistenceTracker{})
	if !errors.Is(fetchErr, whatsmeow.ErrAppStateBacklog) || persistErr != nil {
		t.Fatalf("fetch=%v persist=%v", fetchErr, persistErr)
	}
	if complete, err := a.db.RegularFullSyncComplete(); err != nil || !complete {
		t.Fatalf("refused fetch invalidated verified coverage: complete=%v err=%v", complete, err)
	}
	assertNoStaleFetchDebt(t, a)

	err := a.syncChatStateBeforeWrite(t.Context(), appstate.WAPatchRegular)
	if !errors.Is(err, whatsmeow.ErrAppStateBacklog) || !strings.Contains(err.Error(), "retry the command") {
		t.Fatalf("caller must retry rather than write behind a backlog: %v", err)
	}
	assertNoStaleFetchDebt(t, a)
	noPatch(t, f)

	warnings := 0
	for line := range bytes.SplitSeq(bytes.TrimSpace(eventLog.Bytes()), []byte("\n")) {
		var event struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("invalid lifecycle JSON: %s: %v", line, err)
		}
		if event.Data["code"] != "app_state_backlog" {
			continue
		}
		warnings++
		if event.Data["name"] != "regular" || event.Data["depth"] != float64(64) || event.Data["oldest_age_ms"] != float64(90000) {
			t.Fatalf("backlog warning lacks the queue metrics: %v", event.Data)
		}
	}
	if warnings != 2 {
		t.Fatalf("expected one backlog warning per refused fetch, got %d", warnings)
	}

	f.appStateFetchErr = nil
	if err := a.syncChatStateBeforeWrite(t.Context(), appstate.WAPatchRegular); err != nil {
		t.Fatalf("fetch after the backlog drained: %v", err)
	}
	// The first fetch above was the explicit full replay.
	for _, fetch := range f.appStateFetches[1:] {
		if fetch.fullSync {
			t.Fatal("refused incremental fetch escalated to a full replay")
		}
	}
}
