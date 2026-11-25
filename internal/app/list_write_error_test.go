package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func wantListWriteError(t *testing.T, err error, code, outcome string) {
	t.Helper()
	var typed *ListWriteError
	if !errors.As(err, &typed) {
		t.Fatalf("err = %v, want a ListWriteError %s", err, code)
	}
	if typed.Code != code || typed.Outcome != outcome {
		t.Fatalf("err = %q code=%s outcome=%s, want %s/%s", err, typed.Code, typed.Outcome, code, outcome)
	}
}

func rawWacliDB(t *testing.T, a *App) *sql.DB {
	t.Helper()
	raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	return raw
}

func TestListWriteErrorCodesBeforeSend(t *testing.T) {
	at := time.Unix(100, 0)
	a, f := newLabelWriteApp(t,
		labelEvent("1", "Locked", at, true, func(l *waSyncAction.LabelEditAction) { l.IsImmutable = proto.Bool(true) }),
		labelEvent("2", "Unread", at, true, func(l *waSyncAction.LabelEditAction) { l.Type = waSyncAction.LabelEditAction_UNREAD.Enum() }),
		labelEvent("3", "Old", at, true, nil),
		labelEvent("4", "Twin", at, true, nil),
		labelEvent("5", "Twin", at, true, nil),
		labelEvent("6", "Gone", at, true, func(l *waSyncAction.LabelEditAction) { l.Deleted = proto.Bool(true) }),
		labelEvent("7", "Ventas", at, true, nil),
		&events.AppStateSyncComplete{Name: appstate.WAPatchRegular},
	)
	ctx := t.Context()
	empty := ""
	for _, tc := range []struct {
		name string
		code string
		run  func() error
	}{
		{"duplicate create", ListErrDuplicateName, func() error {
			_, err := a.CreateLabel(ctx, LabelInput{Name: proto.String("Old"), ID: "40"})
			return err
		}},
		{"duplicate rename", ListErrDuplicateName, func() error {
			_, err := a.EditLabel(ctx, "3", LabelInput{Name: proto.String("Ventas")})
			return err
		}},
		{"unknown ref", ListErrNotFound, func() error { _, err := a.EditLabel(ctx, "nope", LabelInput{Color: proto.Int32(1)}); return err }},
		{"unknown chat label ref", ListErrNotFound, func() error {
			_, err := a.SetChatLabel(ctx, types.NewJID("123", types.DefaultUserServer), "nope", true)
			return err
		}},
		{"deleted by ID", ListErrDeleted, func() error { _, err := a.DeleteLabel(ctx, "6"); return err }},
		{"deleted by name", ListErrDeleted, func() error { _, err := a.EditLabel(ctx, "Gone", LabelInput{Color: proto.Int32(1)}); return err }},
		{"ambiguous", ListErrAmbiguousName, func() error { _, err := a.DeleteLabel(ctx, "Twin"); return err }},
		{"id in use", ListErrIDInUse, func() error { _, err := a.CreateLabel(ctx, LabelInput{Name: proto.String("New"), ID: "3"}); return err }},
		{"deleted id in use", ListErrIDInUse, func() error { _, err := a.CreateLabel(ctx, LabelInput{Name: proto.String("New"), ID: "6"}); return err }},
		{"id invalid", ListErrIDInvalid, func() error {
			_, err := a.CreateLabel(ctx, LabelInput{Name: proto.String("New"), ID: "01"})
			return err
		}},
		{"id spaces", ListErrIDInvalid, func() error {
			_, err := a.CreateLabel(ctx, LabelInput{Name: proto.String("New"), ID: " 8"})
			return err
		}},
		{"immutable", ListErrImmutable, func() error { _, err := a.EditLabel(ctx, "1", LabelInput{Name: proto.String("x")}); return err }},
		{"system type", ListErrSystemType, func() error { _, err := a.DeleteLabel(ctx, "2"); return err }},
		{"system type chat", ListErrSystemType, func() error {
			_, err := a.SetChatLabel(ctx, types.NewJID("123", types.DefaultUserServer), "2", true)
			return err
		}},
		{"lid only", ListErrChatLIDOnly, func() error {
			_, err := a.SetChatLabel(ctx, types.NewJID("555", types.HiddenUserServer), "7", true)
			return err
		}},
		{"empty name", ListErrInvalidInput, func() error { _, err := a.CreateLabel(ctx, LabelInput{Name: &empty}); return err }},
		{"missing name", ListErrInvalidInput, func() error { _, err := a.CreateLabel(ctx, LabelInput{}); return err }},
		{"color", ListErrInvalidInput, func() error { _, err := a.EditLabel(ctx, "3", LabelInput{Color: proto.Int32(20)}); return err }},
		{"nothing to change", ListErrInvalidInput, func() error { _, err := a.EditLabel(ctx, "3", LabelInput{}); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantListWriteError(t, tc.run(), tc.code, WriteOutcomeNotSent)
		})
	}
	noPatch(t, f)
}

func TestCreateLabelIndexIncompleteCode(t *testing.T) {
	a, f := newLabelWriteApp(t, labelEvent("3", "Old", time.Unix(100, 0), false, nil))
	_, err := a.CreateLabel(t.Context(), LabelInput{Name: proto.String("Prueba")})
	wantListWriteError(t, err, ListErrIndexIncomplete, WriteOutcomeNotSent)
	noPatch(t, f)
}

func TestCreateLabelWithoutIDHonorsBrokenFullSync(t *testing.T) {
	complete := func() []any {
		return []any{labelEvent("3", "Old", time.Unix(100, 0), true, nil), &events.AppStateSyncComplete{Name: appstate.WAPatchRegular}}
	}
	for _, tc := range []struct {
		name       string
		brokenAt   int64 // seconds; 0 = not broken
		completeAt int64 // milliseconds; 0 = NULL
		allowed    bool
	}{
		{"not broken", 0, 0, true},
		{"broken after complete", 2000, 1_000_500, false},
		{"broken, same second", 1000, 1_000_500, false},
		{"complete recorded after broken", 1000, 1_001_000, true},
		{"broken, completion time unknown", 1000, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, f := newLabelWriteApp(t, complete()...)
			raw := rawWacliDB(t, a)
			if tc.completeAt == 0 {
				if _, err := raw.Exec(`UPDATE list_sync_state SET full_sync_complete_at=NULL`); err != nil {
					t.Fatal(err)
				}
			} else if _, err := raw.Exec(`UPDATE list_sync_state SET full_sync_complete_at=?`, tc.completeAt); err != nil {
				t.Fatal(err)
			}
			if tc.brokenAt != 0 {
				if _, err := raw.Exec(`INSERT INTO app_state_full_sync_broken(collection,marked_at) VALUES('regular',?)`, tc.brokenAt); err != nil {
					t.Fatal(err)
				}
				// Marking broken must not lower the completion bit.
				var bit bool
				if err := raw.QueryRow(`SELECT full_sync_complete FROM list_sync_state`).Scan(&bit); err != nil || !bit {
					t.Fatalf("full_sync_complete = %v, %v", bit, err)
				}
			}
			created, err := a.CreateLabel(t.Context(), LabelInput{Name: proto.String("Prueba")})
			if tc.allowed {
				if err != nil || created.ID != "4" {
					t.Fatalf("created = %+v, %v", created, err)
				}
				return
			}
			wantListWriteError(t, err, ListErrIndexIncomplete, WriteOutcomeNotSent)
			noPatch(t, f)
			// An explicit unused ID stays possible.
			if _, err := a.CreateLabel(t.Context(), LabelInput{Name: proto.String("Prueba"), ID: "40"}); err != nil {
				t.Fatalf("explicit ID: %v", err)
			}
		})
	}
}

func TestCreateLabelWithoutIDRefusesPendingRecoveryDebt(t *testing.T) {
	a, f := newLabelWriteApp(t, labelEvent("3", "Old", time.Unix(100, 0), true, nil), &events.AppStateSyncComplete{Name: appstate.WAPatchRegular})
	// A degraded pre-write sync clears only its own marker and keeps older debt.
	f.appStateLIDMigrated, f.hasLocalAppState = true, true
	if _, err := a.db.MarkAppStateRecoveryGeneration(string(appstate.WAPatchRegular)); err != nil {
		t.Fatal(err)
	}
	_, err := a.CreateLabel(t.Context(), LabelInput{Name: proto.String("Prueba")})
	wantListWriteError(t, err, ListErrIndexIncomplete, WriteOutcomeNotSent)
	noPatch(t, f)
}

func TestListWriteCoordinatorUnavailableIsNotSent(t *testing.T) {
	a, f := newLabelWriteApp(t, labelEvent("7", "Ventas", time.Unix(100, 0), true, nil))
	t.Run("coordinator busy until the deadline", func(t *testing.T) {
		<-a.chatStateSync
		defer func() { a.chatStateSync <- struct{}{} }()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		defer cancel()
		_, err := a.DeleteLabel(ctx, "7")
		wantListWriteError(t, err, ListErrCoordinatorDown, WriteOutcomeNotSent)
	})
	t.Run("pre-write sync fails", func(t *testing.T) {
		f.appStateFetchErr = errors.New("sync boom")
		defer func() { f.appStateFetchErr = nil }()
		_, err := a.DeleteLabel(t.Context(), "7")
		wantListWriteError(t, err, ListErrCoordinatorDown, WriteOutcomeNotSent)
	})
	noPatch(t, f)
}

func TestListWriteSendFailuresAreUnknownOutcome(t *testing.T) {
	t.Run("send error", func(t *testing.T) {
		a, f := newLabelWriteApp(t, labelEvent("7", "Ventas", time.Unix(100, 0), true, nil))
		f.appStatePatchErr = errors.New("server rejected patch")
		_, err := a.EditLabel(t.Context(), "7", LabelInput{Name: proto.String("Otro")})
		wantListWriteError(t, err, ListErrWriteOutcomeUnknown, WriteOutcomeUnknown)
		if !errors.Is(err, f.appStatePatchErr) {
			t.Fatalf("lost the cause: %v", err)
		}
	})
	t.Run("missing apply boundary", func(t *testing.T) {
		a, f := newLabelWriteApp(t, labelEvent("7", "Ventas", time.Unix(100, 0), true, nil))
		a.wa = noBoundaryWA{f}
		_, err := a.DeleteLabel(t.Context(), "7")
		wantListWriteError(t, err, ListErrWriteOutcomeUnknown, WriteOutcomeUnknown)
	})
	t.Run("persistence failure after send", func(t *testing.T) {
		a, f := newLabelWriteApp(t, labelEvent("7", "Ventas", time.Unix(100, 0), true, nil))
		if _, err := rawWacliDB(t, a).Exec(`DROP TABLE chat_lists`); err != nil {
			t.Fatal(err)
		}
		_, err := a.SetChatLabel(t.Context(), types.NewJID("123", types.DefaultUserServer), "7", true)
		wantListWriteError(t, err, ListErrWriteOutcomeUnknown, WriteOutcomeUnknown)
		onlyPatch(t, f)
	})
}

type noBoundaryWA struct{ *fakeWA }

func (noBoundaryWA) SendAppStatePatch(context.Context, appstate.PatchInfo, func()) ([]any, error) {
	return nil, nil
}

func TestListWriteResultsReportStoredUpdatedAt(t *testing.T) {
	a, f := newLabelWriteApp(t, labelEvent("3", "Old", time.Unix(100, 0), true, nil), &events.AppStateSyncComplete{Name: appstate.WAPatchRegular})
	raw := rawWacliDB(t, a)
	stored := func(id string) int64 {
		var at int64
		if err := raw.QueryRow(`SELECT updated_at FROM lists WHERE id=?`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	created, err := a.CreateLabel(t.Context(), LabelInput{Name: proto.String("Prueba")})
	if err != nil || created.UpdatedAt == 0 || created.UpdatedAt != stored(created.ID) {
		t.Fatalf("created = %+v (stored %d), %v", created, stored("4"), err)
	}
	if want := f.appStatePatches[0].Timestamp.UnixMilli(); created.UpdatedAt != want {
		t.Fatalf("updated_at = %d, want the patch timestamp %d", created.UpdatedAt, want)
	}
	time.Sleep(2 * time.Millisecond)
	edited, err := a.EditLabel(t.Context(), "4", LabelInput{Color: proto.Int32(3)})
	if err != nil || edited.UpdatedAt <= created.UpdatedAt || edited.UpdatedAt != stored("4") {
		t.Fatalf("edited = %+v (stored %d), %v", edited, stored("4"), err)
	}
	time.Sleep(2 * time.Millisecond)
	deleted, err := a.DeleteLabel(t.Context(), "4")
	if err != nil || deleted.UpdatedAt <= edited.UpdatedAt || deleted.UpdatedAt != stored("4") {
		t.Fatalf("deleted = %+v (stored %d), %v", deleted, stored("4"), err)
	}
	pn := types.NewJID("123", types.DefaultUserServer)
	for _, labeled := range []bool{true, false} {
		time.Sleep(2 * time.Millisecond)
		result, err := a.SetChatLabel(t.Context(), pn, "3", labeled)
		var at int64
		if qerr := raw.QueryRow(`SELECT updated_at FROM chat_lists WHERE chat_jid=? AND list_id='3'`, pn.String()).Scan(&at); qerr != nil {
			t.Fatal(qerr)
		}
		if err != nil || result.UpdatedAt == 0 || result.UpdatedAt != at {
			t.Fatalf("chat label %t = %+v (stored %d), %v", labeled, result, at, err)
		}
	}
}

func TestListWriteUpdatedAtIsTheStoredValueWhenALocalRowIsNewer(t *testing.T) {
	// A phone clock ahead of this machine leaves the newer row in place; the
	// result must report what is stored, not the discarded patch timestamp.
	future := time.Now().Add(time.Hour)
	a, _ := newLabelWriteApp(t, labelEvent("7", "Ventas", future, true, nil))
	edited, err := a.EditLabel(t.Context(), "7", LabelInput{Color: proto.Int32(3)})
	if err != nil || edited.UpdatedAt != future.UnixMilli() {
		t.Fatalf("edited = %+v, %v; want stored %d", edited, err, future.UnixMilli())
	}
}
