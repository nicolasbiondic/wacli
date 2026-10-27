package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

type fakeGroupDelegateClient struct {
	group     types.JID
	refreshed types.JID
	users     []types.JID
	action    wa.GroupParticipantAction
	reset     bool
	calls     int
	updated   []types.GroupParticipant
	info      *types.GroupInfo
	err       error
}

func (f *fakeGroupDelegateClient) ResolveLIDToPN(_ context.Context, jid types.JID) types.JID {
	return jid
}

func (f *fakeGroupDelegateClient) UpdateGroupParticipants(_ context.Context, group types.JID, users []types.JID, action wa.GroupParticipantAction) ([]types.GroupParticipant, error) {
	f.group, f.users, f.action = group, users, action
	f.calls++
	return f.updated, f.err
}

func (f *fakeGroupDelegateClient) GetGroupInfo(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
	f.refreshed = jid
	return f.info, nil
}

func TestGroupTargetValidationAndCanonicalExecution(t *testing.T) {
	for _, raw := range []string{"", "123", "123@s.whatsapp.net", "123@lid", "@g.us", "123@g.us@evil", "123@@g.us", "a@g.us", " 123@g.us", "123@g.us ", "123:bad@g.us", "123:-1@g.us", "123:65536@g.us", "123.256:0@g.us", "123.:0@g.us", "123.0@g.us", "123_0:0@g.us", "123:0:1@g.us", "123--4@g.us"} {
		t.Run(raw, func(t *testing.T) {
			var order groupDelegateOrder
			if release, err := order.acquire(t.Context(), raw); err == nil {
				release()
				t.Fatal("invalid target acquired a slot")
			}
			if len(order.groups) != 0 {
				t.Fatal("invalid target allocated slot")
			}
			for _, req := range groupDelegateTestRequests() {
				req.JID = raw
				client := &fakeGroupDelegateClient{}
				if _, err := executeGroupDelegateTest(t.Context(), nil, client, req); err == nil || client.calls != 0 {
					t.Fatalf("invalid target reached client: %+v, %v", req, err)
				}
			}
		})
	}
	for _, raw := range []string{"123@g.us", "123:0@g.us", "123.0:65535@g.us", "123.255:1@g.us", "123-456:0@g.us"} {
		want := "123@g.us"
		if strings.HasPrefix(raw, "123-456") {
			want = "123-456@g.us"
		}
		for _, req := range groupDelegateTestRequests() {
			req.JID = raw
			client := &fakeGroupDelegateClient{}
			resp, err := executeGroupDelegateTest(t.Context(), nil, client, req)
			if err != nil || client.group.String() != want {
				t.Fatalf("target %s = %+v, %v", raw, client, err)
			}
			if req.Kind == groupParticipantsKind && client.refreshed.String() != want {
				t.Fatal("refresh used a different target")
			}
			if req.Kind == groupInviteLinkKind && resp.GroupInviteLink.JID != want {
				t.Fatal("result was not canonical")
			}
		}
	}
}

func (f *fakeGroupDelegateClient) GetGroupInviteLink(_ context.Context, group types.JID, reset bool) (string, error) {
	f.group, f.reset = group, reset
	f.calls++
	return "https://chat.whatsapp.com/test-only", f.err
}

func groupDelegateTestRequests() []sendDelegateRequest {
	var requests []sendDelegateRequest
	for _, action := range []string{"add", "remove", "promote", "demote"} {
		requests = append(requests, sendDelegateRequest{Kind: groupParticipantsKind, JID: "123@g.us", GroupAction: action, Users: []string{"+1 (234) 567-8900", "222@lid"}})
	}
	for _, action := range []string{"get", "revoke"} {
		requests = append(requests, sendDelegateRequest{Kind: groupInviteLinkKind, JID: "123@g.us", GroupAction: action})
	}
	return requests
}

func groupDelegateTestArgs(req sendDelegateRequest) []string {
	args := []string{"groups"}
	if req.Kind == groupParticipantsKind {
		args = append(args, "participants", req.GroupAction)
	} else {
		args = append(args, "invite", "link", req.GroupAction)
	}
	args = append(args, "--jid", req.JID)
	for _, user := range req.Users {
		args = append(args, "--user", user)
	}
	return args
}

func executeGroupDelegateTest(ctx context.Context, db *store.DB, client *fakeGroupDelegateClient, req sendDelegateRequest) (sendDelegateResponse, error) {
	if req.Kind == groupParticipantsKind {
		updated, err := executeGroupParticipants(ctx, db, client, req)
		return sendDelegateResponse{OK: err == nil, GroupParticipants: &groupParticipantsResult{Updated: updated}}, err
	}
	result, err := executeGroupInviteLink(ctx, client, req)
	return sendDelegateResponse{OK: err == nil, GroupInviteLink: result}, err
}

func TestGroupsDelegateThroughProductionServerWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, req := range groupDelegateTestRequests() {
		t.Run(req.Kind+"/"+req.GroupAction, func(t *testing.T) {
			storeDir := shortPresenceDelegateStoreDir(t)
			lk, err := lock.Acquire(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			defer lk.Release()
			client := &fakeGroupDelegateClient{updated: []types.GroupParticipant{
				{JID: types.NewJID("12345678900", types.DefaultUserServer), IsAdmin: true},
				{JID: types.NewJID("222", types.HiddenUserServer), Error: 403, AddRequest: &types.GroupParticipantAddRequest{Code: "test", Expiration: time.Unix(1700000000, 0).UTC()}},
			}}
			requests := make(chan sendDelegateRequest, 1)
			stop, err := startSendDelegateServerForStore(t.Context(), storeDir, sendSpacing{}, func(ctx context.Context, got sendDelegateRequest) (sendDelegateResponse, error) {
				resp, err := executeGroupDelegateTest(ctx, nil, client, got)
				requests <- got
				return resp, err
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			args := append([]string{"--store", storeDir, "--json", "--timeout", "750ms"}, groupDelegateTestArgs(req)...)
			stdout, stderr, err := runPresenceDelegateHelper(t, args)
			if err != nil {
				t.Fatalf("delegation failed: %v stdout=%s stderr=%s", err, stdout, stderr)
			}
			got := <-requests
			if got.Kind != req.Kind || got.Version != sendDelegateVersion || got.GroupAction != req.GroupAction || got.JID != req.JID || !reflect.DeepEqual(got.Users, req.Users) || got.TimeoutMS != 750 || got.DeadlineUnixMS == 0 {
				t.Fatalf("request = %+v, want %+v with version and deadline", got, req)
			}
			// The standalone and delegated paths share execution and output. Compare
			// raw JSON as well, so Error, AddRequest and nil/empty arrays cannot drift.
			var direct string
			if req.Kind == groupParticipantsKind {
				if client.group.String() != req.JID || client.action != wa.GroupParticipantAction(req.GroupAction) || len(client.users) != 2 || client.users[0].String() != "12345678900@s.whatsapp.net" || client.users[1].String() != "222@lid" {
					t.Fatalf("participant call = %+v", client)
				}
				updated, err := executeGroupParticipants(t.Context(), nil, client, req)
				if err != nil {
					t.Fatal(err)
				}
				direct = captureRootStdout(t, func() { _ = writeGroupParticipantsResult(&rootFlags{asJSON: true}, updated) })
				if !strings.Contains(stdout, `"Error":403`) {
					t.Fatalf("participant error lost: %s", stdout)
				}
			} else {
				if client.group.String() != req.JID || client.reset != (req.GroupAction == "revoke") {
					t.Fatalf("invite call = %+v", client)
				}
				result, err := executeGroupInviteLink(t.Context(), client, req)
				if err != nil {
					t.Fatal(err)
				}
				direct = captureRootStdout(t, func() { _ = writeGroupInviteLinkResult(&rootFlags{asJSON: true}, result) })
			}
			// The subprocess test harness appends PASS after the command output.
			commandJSON, _, _ := strings.Cut(stdout, "\n")
			if commandJSON != strings.TrimSpace(direct) {
				t.Fatalf("delegated=%s standalone=%s", stdout, direct)
			}
		})
	}
}

func TestGroupsDelegateOldOwnerAndOperationErrors(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, req := range groupDelegateTestRequests() {
		for _, oldOwner := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%s/old=%t", req.Kind, req.GroupAction, oldOwner), func(t *testing.T) {
				storeDir := shortPresenceDelegateStoreDir(t)
				lk, err := lock.Acquire(storeDir)
				if err != nil {
					t.Fatal(err)
				}
				defer lk.Release()
				stop, err := startSendDelegateServerForStore(t.Context(), storeDir, sendSpacing{}, func(ctx context.Context, got sendDelegateRequest) (sendDelegateResponse, error) {
					if oldOwner {
						// The base executable has this exact default switch rejection.
						return sendDelegateResponse{}, fmt.Errorf("unsupported send kind %q", got.Kind)
					}
					return executeGroupDelegateTest(ctx, nil, &fakeGroupDelegateClient{err: errors.New("permission denied")}, got)
				})
				if err != nil {
					t.Fatal(err)
				}
				defer stop()
				args := append([]string{"--store", storeDir, "--json", "--timeout", "750ms"}, groupDelegateTestArgs(req)...)
				stdout, stderr, err := runPresenceDelegateHelper(t, args)
				if err == nil || strings.Contains(stdout, `"success":true`) {
					t.Fatalf("unexpected success: %v %s %s", err, stdout, stderr)
				}
				if oldOwner {
					for _, want := range []string{"unsupported send kind", req.Kind, "delegated groups", "restart `wacli sync` after upgrading"} {
						if !strings.Contains(stderr, want) {
							t.Fatalf("missing %q: %s", want, stderr)
						}
					}
				} else if !strings.Contains(stderr, "permission denied") || strings.Contains(stderr, "after upgrading") {
					t.Fatalf("operation error changed: %s", stderr)
				}
			})
		}
	}
}

func TestGroupsStandaloneReadOnlyAndMissingSocket(t *testing.T) {
	for _, req := range groupDelegateTestRequests() {
		for _, mode := range []string{"local", "read-only", "locked-no-socket"} {
			t.Run(req.Kind+"/"+req.GroupAction+"/"+mode, func(t *testing.T) {
				storeDir := t.TempDir()
				args := []string{"--store", storeDir, "--json"}
				want := "not authenticated"
				if mode == "read-only" {
					args = append(args, "--read-only")
					want = "read-only mode"
				}
				if mode != "local" {
					lk, err := lock.Acquire(storeDir)
					if err != nil {
						t.Fatal(err)
					}
					defer lk.Release()
					if mode == "locked-no-socket" {
						want = "store is locked"
					}
				}
				var commandErr error
				captureRootStderr(t, func() { commandErr = execute(append(args, groupDelegateTestArgs(req)...)) })
				if commandErr == nil || !strings.Contains(commandErr.Error(), want) {
					t.Fatalf("error=%v, want %s", commandErr, want)
				}
			})
		}
	}
}

func TestGroupParticipantsSnapshotAndValidation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &fakeGroupDelegateClient{info: &types.GroupInfo{JID: types.NewJID("123", types.GroupServer), Participants: []types.GroupParticipant{{JID: types.NewJID("222", types.DefaultUserServer), IsAdmin: true}}}}
	req := groupDelegateTestRequests()[0]
	if _, err := executeGroupParticipants(t.Context(), db, client, req); err != nil {
		t.Fatal(err)
	}
	participants, err := db.ListGroupParticipants(req.JID)
	if err != nil || len(participants) != 1 || participants[0].Role != "admin" {
		t.Fatalf("snapshot = %+v, err=%v", participants, err)
	}
	for _, bad := range []sendDelegateRequest{
		{GroupAction: "delete", JID: req.JID, Users: req.Users},
		{GroupAction: "add", JID: req.JID},
		{GroupAction: "add", Users: req.Users},
		{GroupAction: "add", JID: req.JID, Users: []string{"not-a-phone"}},
	} {
		before := client.calls
		if _, err := executeGroupParticipants(t.Context(), db, client, bad); err == nil || client.calls != before {
			t.Fatalf("invalid request reached WhatsApp: %+v err=%v", bad, err)
		}
	}
	for _, action := range []string{"", "reset", "delete"} {
		before := client.calls
		if _, err := executeGroupInviteLink(t.Context(), client, sendDelegateRequest{JID: req.JID, GroupAction: action}); err == nil || client.calls != before {
			t.Fatalf("invalid invite action %q accepted", action)
		}
	}
}

func TestGroupsDelegateQueueAndDeadline(t *testing.T) {
	for _, kind := range []string{groupParticipantsKind, groupInviteLinkKind} {
		for _, mode := range []string{"bypass-queue", "expired", "uncertain"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				serverConn, clientConn := net.Pipe()
				defer clientConn.Close()
				_ = clientConn.SetDeadline(time.Now().Add(2 * time.Second))
				calls := make(chan struct{}, 1)
				go handleSendDelegateConn(t.Context(), serverConn, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
					calls <- struct{}{}
					if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 750*time.Millisecond {
						return sendDelegateResponse{}, errors.New("missing or extended deadline")
					}
					if mode == "uncertain" {
						return sendDelegateResponse{}, context.DeadlineExceeded
					}
					return sendDelegateResponse{OK: true}, nil
				}, make(chan struct{}, 1), newSendPacer(sendSpacing{min: time.Second, max: time.Second}))
				req := sendDelegateRequest{Kind: kind, Version: sendDelegateVersion, TimeoutMS: 750}
				if mode == "expired" {
					req.DeadlineUnixMS = time.Now().Add(-time.Second).UnixMilli()
				}
				if err := json.NewEncoder(clientConn).Encode(req); err != nil {
					t.Fatal(err)
				}
				var resp sendDelegateResponse
				if err := json.NewDecoder(clientConn).Decode(&resp); err != nil {
					t.Fatalf("request waited for occupied send slot: %v", err)
				}
				if mode == "expired" {
					if resp.OK || !strings.Contains(resp.Error, "it was not sent") || len(calls) != 0 {
						t.Fatalf("expired request ran: %+v", resp)
					}
				} else if mode == "uncertain" {
					if resp.OK || !strings.Contains(resp.Error, "may still have gone through; check before retrying") {
						t.Fatalf("uncertainty lost: %+v", resp)
					}
				} else if !resp.OK || len(calls) != 1 {
					t.Fatalf("not dispatched: %+v", resp)
				}
			})
		}
	}
}

func TestGroupParticipantsIPCArrayShape(t *testing.T) {
	for _, updated := range [][]types.GroupParticipant{nil, {}} {
		raw, err := json.Marshal(sendDelegateResponse{OK: true, GroupParticipants: &groupParticipantsResult{Updated: updated}})
		if err != nil {
			t.Fatal(err)
		}
		var resp sendDelegateResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatal(err)
		}
		if resp.GroupParticipants == nil || !reflect.DeepEqual(resp.GroupParticipants.Updated, updated) {
			t.Fatalf("null/empty array lost: %s", raw)
		}
	}
}

func TestGroupsDelegateMissingResultsAndHelp(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, req := range groupDelegateTestRequests() {
		t.Run(req.Kind+"/"+req.GroupAction, func(t *testing.T) {
			storeDir := shortPresenceDelegateStoreDir(t)
			lk, err := lock.Acquire(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			defer lk.Release()
			stop, err := startSendDelegateServerForStore(t.Context(), storeDir, sendSpacing{}, func(context.Context, sendDelegateRequest) (sendDelegateResponse, error) {
				return sendDelegateResponse{OK: true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			args := append([]string{"--store", storeDir, "--json"}, groupDelegateTestArgs(req)...)
			stdout, stderr, err := runPresenceDelegateHelper(t, args)
			if err == nil || !strings.Contains(stderr, "did not report") || !strings.Contains(stderr, "restart `wacli sync`") || strings.Contains(stdout, `"success":true`) {
				t.Fatalf("missing result reported as success: %v %s %s", err, stdout, stderr)
			}
			args = append(groupDelegateTestArgs(req), "--help")
			stdout, stderr, err = runPresenceDelegateHelper(t, args)
			if err != nil || !strings.Contains(stdout, "Delegates to the running sync process when the store is locked.") {
				t.Fatalf("missing help capability marker: %v %s %s", err, stdout, stderr)
			}
		})
	}
}

func TestGroupDelegateOrderCancellationAndCleanup(t *testing.T) {
	var order groupDelegateOrder
	release, err := order.acquire(t.Context(), "123@g.us")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := order.acquire(ctx, "123@g.us"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled group waiter = %v", err)
	}
	other, err := order.acquire(t.Context(), "456@g.us")
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	if len(order.groups) != 0 {
		t.Fatalf("unused group slots leaked: %d", len(order.groups))
	}
}

func TestGroupCommandsSerializeSameGroupButNotOtherGroups(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	started := make(chan string, 3)
	finishFirst := make(chan struct{})
	stop, err := startSendDelegateServerForStore(t.Context(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		started <- req.ID
		if req.ID == "first" {
			select {
			case <-finishFirst:
			case <-ctx.Done():
				return sendDelegateResponse{}, ctx.Err()
			}
		}
		return sendDelegateResponse{OK: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	request := func(id, group, kind string) <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := delegateSend(t.Context(), &rootFlags{storeDir: storeDir, timeout: 2 * time.Second}, sendDelegateRequest{Kind: kind, JID: group, ID: id})
			done <- err
		}()
		return done
	}
	first := request("first", "123@g.us", groupParticipantsKind)
	if id := <-started; id != "first" {
		t.Fatal(id)
	}
	second := request("second", "123:0@g.us", groupInviteLinkKind)
	other := request("other", "456@g.us", groupParticipantsKind)
	if err := <-other; err != nil {
		t.Fatal(err)
	}
	if id := <-started; id != "other" {
		t.Fatalf("same-group invite overtook the in-flight snapshot refresh: %s", id)
	}
	select {
	case id := <-started:
		t.Fatalf("same-group operation started before refresh completed: %s", id)
	case <-time.After(50 * time.Millisecond):
	}
	close(finishFirst)
	for _, done := range []<-chan error{first, second} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if id := <-started; id != "second" {
		t.Fatal(id)
	}
}
