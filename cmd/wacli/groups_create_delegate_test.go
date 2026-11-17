package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/delegate"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type fakeGroupCreateClient struct {
	req   wa.CreateGroupRequest
	calls int
	info  *types.GroupInfo
	err   error
}

func (f *fakeGroupCreateClient) ResolveLIDToPN(_ context.Context, jid types.JID) types.JID {
	return jid
}

func (f *fakeGroupCreateClient) CreateGroup(_ context.Context, req wa.CreateGroupRequest) (*types.GroupInfo, error) {
	f.req = req
	f.calls++
	return f.info, f.err
}

// A created group as WhatsApp reports it: local-zone timestamps, LID and phone
// identities, a participant that could not be added, and characters JSON
// escapes. All of it must survive the IPC hop unchanged.
func groupCreateTestInfo() *types.GroupInfo {
	created := time.Unix(1700000000, 0)
	return &types.GroupInfo{
		JID:      types.NewJID("120363000000000001", types.GroupServer),
		OwnerJID: types.NewJID("999", types.HiddenUserServer),
		OwnerPN:  types.NewJID("15550000000", types.DefaultUserServer),
		GroupName: types.GroupName{
			Name:      `Nuevo grupo <&> "ñ"`,
			NameSetAt: created,
			NameSetBy: types.NewJID("999", types.HiddenUserServer),
		},
		GroupAnnounce:               types.GroupAnnounce{IsAnnounce: true, AnnounceVersionID: "v1"},
		GroupLocked:                 types.GroupLocked{IsLocked: true},
		GroupLinkedParent:           types.GroupLinkedParent{LinkedParentJID: types.NewJID("120363000000000999", types.GroupServer)},
		GroupMembershipApprovalMode: types.GroupMembershipApprovalMode{IsJoinApprovalRequired: true},
		AddressingMode:              types.AddressingModeLID,
		GroupCreated:                created,
		Participants: []types.GroupParticipant{
			{JID: types.NewJID("999", types.HiddenUserServer), PhoneNumber: types.NewJID("15550000000", types.DefaultUserServer), LID: types.NewJID("999", types.HiddenUserServer), IsAdmin: true, IsSuperAdmin: true},
			{JID: types.NewJID("12345678900", types.DefaultUserServer), PhoneNumber: types.NewJID("12345678900", types.DefaultUserServer)},
			{JID: types.NewJID("222", types.HiddenUserServer), LID: types.NewJID("222", types.HiddenUserServer), Error: 403, AddRequest: &types.GroupParticipantAddRequest{Code: "invite-code", Expiration: time.Unix(1700086400, 0)}},
		},
		ParticipantCount: 3,
		MemberAddMode:    types.GroupMemberAddModeAdmin,
	}
}

func groupCreateTestRequest() groupCreateRequest {
	return groupCreateRequest{
		Name:         "Nuevo grupo",
		Users:        []string{"+1 (234) 567-8900", "222@lid"},
		AnnounceOnly: true,
		Locked:       true,
		JoinApproval: true,
		LinkedParent: "120363000000000999@g.us",
	}
}

func groupCreateTestArgs() []string {
	return []string{
		"groups", "create", "--name", "Nuevo grupo",
		"--user", "+1 (234) 567-8900", "--user", "222@lid",
		"--announce-only", "--locked", "--join-approval",
		"--linked-parent", "120363000000000999@g.us",
	}
}

func openGroupCreateTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func lockedGroupCreateStore(t *testing.T) string {
	t.Helper()
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lk.Release() })
	return storeDir
}

func TestGroupCreateDelegateJSONRoundTrip(t *testing.T) {
	create := groupCreateTestRequest()
	create.Community = true
	raw, err := json.Marshal(sendDelegateRequest{Version: sendDelegateVersion, Kind: groupCreateKind, GroupCreate: &create})
	if err != nil {
		t.Fatal(err)
	}
	var req sendDelegateRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.Kind != groupCreateKind || req.GroupCreate == nil || !reflect.DeepEqual(*req.GroupCreate, create) {
		t.Fatalf("request round trip = %s -> %+v", raw, req.GroupCreate)
	}

	for _, info := range []*types.GroupInfo{groupCreateTestInfo(), nil} {
		raw, err := json.Marshal(sendDelegateResponse{OK: true, GroupCreate: &groupCreateResult{Info: info}})
		if err != nil {
			t.Fatal(err)
		}
		var resp sendDelegateResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatal(err)
		}
		if resp.GroupCreate == nil {
			t.Fatalf("result lost: %s", raw)
		}
		want, _ := json.Marshal(info)
		got, _ := json.Marshal(resp.GroupCreate.Info)
		if string(got) != string(want) {
			t.Fatalf("group info changed over IPC:\n got %s\nwant %s", got, want)
		}
	}
}

func TestExecuteGroupCreateCallsOnceAndPersists(t *testing.T) {
	db := openGroupCreateTestDB(t)
	client := &fakeGroupCreateClient{info: groupCreateTestInfo()}
	info, err := executeGroupCreate(t.Context(), db, client, groupCreateTestRequest())
	if err != nil || info != client.info {
		t.Fatalf("info=%v err=%v", info, err)
	}
	want := wa.CreateGroupRequest{
		Name:                   "Nuevo grupo",
		Participants:           []types.JID{types.NewJID("12345678900", types.DefaultUserServer), types.NewJID("222", types.HiddenUserServer)},
		IsAnnounce:             true,
		IsLocked:               true,
		IsJoinApprovalRequired: true,
		LinkedParentJID:        types.NewJID("120363000000000999", types.GroupServer),
	}
	if client.calls != 1 || !reflect.DeepEqual(client.req, want) {
		t.Fatalf("calls=%d req=%+v, want %+v", client.calls, client.req, want)
	}
	participants, err := db.ListGroupParticipants(client.info.JID.String())
	if err != nil || len(participants) != 3 {
		t.Fatalf("snapshot = %+v, err=%v", participants, err)
	}

	// Even an error that would make a send retry is final for a create.
	for _, failure := range []error{errors.New("websocket disconnected before info query returned response"), errors.New("info query timed out"), context.DeadlineExceeded} {
		client := &fakeGroupCreateClient{err: failure}
		if _, err := executeGroupCreate(t.Context(), db, client, groupCreateTestRequest()); !errors.Is(err, failure) || client.calls != 1 {
			t.Fatalf("create retried or error changed: calls=%d err=%v", client.calls, err)
		}
	}
}

func TestGroupCreateValidationHappensBeforeDispatch(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no name", []string{"groups", "create", "--user", "+12345678900"}, "--name is required"},
		{"blank name", []string{"groups", "create", "--name", "  "}, "--name is required"},
		{"community and parent", []string{"groups", "create", "--name", "x", "--community", "--linked-parent", "120363000000000999@g.us"}, "--community and --linked-parent cannot be combined"},
		{"bad user", []string{"groups", "create", "--name", "x", "--user", "not-a-phone"}, "invalid phone number"},
		{"bad parent", []string{"groups", "create", "--name", "x", "--user", "+12345678900", "--linked-parent", "123@s.whatsapp.net"}, "parse --linked-parent"},
		{"no users", []string{"groups", "create", "--name", "x"}, "invalid user list: at least one --user is required"},
		{"too many users", append([]string{"groups", "create", "--name", "x"}, groupCreateUserArgs(51)...), "invalid user list: at most 50 participants per group, got 51"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storeDir := lockedGroupCreateStore(t)
			var dispatched atomic.Int32
			stop, err := startSendDelegateServerForStore(t.Context(), storeDir, sendSpacing{}, func(context.Context, sendDelegateRequest) (sendDelegateResponse, error) {
				dispatched.Add(1)
				return sendDelegateResponse{OK: true, GroupCreate: &groupCreateResult{}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			stdout, stderr, err := runPresenceDelegateHelper(t, append([]string{"--store", storeDir, "--json"}, tc.args...))
			if err == nil || !strings.Contains(stderr, tc.want) || strings.Contains(stdout, `"success":true`) {
				t.Fatalf("err=%v stdout=%q stderr=%q, want %q", err, stdout, stderr, tc.want)
			}
			if n := dispatched.Load(); n != 0 {
				t.Fatalf("invalid create reached the sync owner %d times", n)
			}

			// The owner validates again; nothing invalid reaches WhatsApp.
			client := &fakeGroupCreateClient{}
			create := groupCreateRequest{Name: "x", Users: []string{"+12345678900"}}
			switch tc.name {
			case "no name":
				create.Name = ""
			case "blank name":
				create.Name = "  "
			case "community and parent":
				create.Community, create.LinkedParent = true, "120363000000000999@g.us"
			case "bad user":
				create.Users = []string{"not-a-phone"}
			case "bad parent":
				create.LinkedParent = "123@s.whatsapp.net"
			case "no users":
				create.Users = nil
			case "too many users":
				create.Users = groupCreatePhones(51)
			}
			if _, err := executeGroupCreate(t.Context(), nil, client, create); err == nil || !strings.Contains(err.Error(), tc.want) || client.calls != 0 {
				t.Fatalf("owner accepted invalid create: err=%v calls=%d", err, client.calls)
			}
		})
	}
}

func TestGroupCreateDelegatesThroughProductionServerWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, asJSON := range []bool{true, false} {
		t.Run(map[bool]string{true: "json", false: "text"}[asJSON], func(t *testing.T) {
			storeDir := lockedGroupCreateStore(t)
			db := openGroupCreateTestDB(t)
			client := &fakeGroupCreateClient{info: groupCreateTestInfo()}
			requests := make(chan sendDelegateRequest, 1)
			stop, err := startSendDelegateServerForStore(t.Context(), storeDir, sendSpacing{}, func(ctx context.Context, got sendDelegateRequest) (sendDelegateResponse, error) {
				requests <- got
				info, err := executeGroupCreate(ctx, db, client, *got.GroupCreate)
				return sendDelegateResponse{OK: err == nil, GroupCreate: &groupCreateResult{Info: info}}, err
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			args := []string{"--store", storeDir, "--timeout", "750ms"}
			if asJSON {
				args = append(args, "--json")
			}
			stdout, stderr, err := runPresenceDelegateHelper(t, append(args, groupCreateTestArgs()...))
			if err != nil {
				t.Fatalf("delegation failed: %v stdout=%s stderr=%s", err, stdout, stderr)
			}
			got := <-requests
			if got.Kind != groupCreateKind || got.Version != sendDelegateVersion || got.TimeoutMS != 750 || got.DeadlineUnixMS == 0 || got.GroupCreate == nil || !reflect.DeepEqual(*got.GroupCreate, groupCreateTestRequest()) {
				t.Fatalf("request = %+v (%+v)", got, got.GroupCreate)
			}
			if client.calls != 1 {
				t.Fatalf("CreateGroup calls = %d", client.calls)
			}
			if strings.Contains(stderr, "store is locked") {
				t.Fatalf("delegated command tried the direct store path: %s", stderr)
			}
			// Byte-for-byte the standalone output for the same group.
			direct := captureRootStdout(t, func() { _ = writeGroupCreateResult(&rootFlags{asJSON: asJSON}, groupCreateTestInfo()) })
			// The subprocess test harness appends PASS after the command output.
			commandOutput := strings.TrimSuffix(stdout, "PASS\n")
			if commandOutput != direct {
				t.Fatalf("delegated=%q standalone=%q", commandOutput, direct)
			}
			if asJSON && (!strings.Contains(commandOutput, `"JID":"120363000000000001@g.us"`) || !strings.Contains(commandOutput, `"Error":403`)) {
				t.Fatalf("gateway fields missing: %s", commandOutput)
			}
		})
	}
}

func TestGroupCreateStandaloneReadOnlyAndMissingSocket(t *testing.T) {
	for _, mode := range []string{"local", "read-only", "locked-no-socket"} {
		t.Run(mode, func(t *testing.T) {
			storeDir := t.TempDir()
			args := []string{"--store", storeDir, "--json"}
			want := "not authenticated"
			if mode == "read-only" {
				args = append(args, "--read-only")
				want = "read-only mode"
			}
			if mode == "locked-no-socket" {
				lk, err := lock.Acquire(storeDir)
				if err != nil {
					t.Fatal(err)
				}
				defer lk.Release()
				want = "store is locked"
			}
			var commandErr error
			captureRootStderr(t, func() { commandErr = execute(append(args, groupCreateTestArgs()...)) })
			if commandErr == nil || !strings.Contains(commandErr.Error(), want) {
				t.Fatalf("error=%v, want %s", commandErr, want)
			}
		})
	}
}

// A create is not idempotent. Once the request reached the owner, a lost or
// late reply is "unknown": the CLI says so, never falls back to the local
// path and never sends the request again.
func TestGroupCreateLostReplyIsUnknownAndNeverRetried(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, mode := range []string{"eof", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			storeDir := lockedGroupCreateStore(t)
			ln, err := delegate.Listen(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			var accepted atomic.Int32
			release := make(chan struct{})
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					accepted.Add(1)
					var req sendDelegateRequest
					_ = json.NewDecoder(conn).Decode(&req)
					if mode == "timeout" {
						<-release
					}
					// Model owner death after accepting the request.
					_ = conn.Close()
				}
			}()
			defer close(release)
			stdout, stderr, err := runPresenceDelegateHelper(t, append([]string{"--store", storeDir, "--json", "--timeout", "500ms"}, groupCreateTestArgs()...))
			want := "lost reply from the running sync process after dispatch; the group_create may still have gone through, so check before retrying"
			if mode == "timeout" {
				want = "no reply from the running sync process before the timeout; the group_create may still have gone through, so check before retrying"
			}
			if err == nil || !strings.Contains(stderr, want) || strings.Contains(stdout, `"success":true`) {
				t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout, stderr)
			}
			if strings.Contains(stderr, "store is locked") || strings.Contains(stderr, "not authenticated") {
				t.Fatalf("lost reply fell back to the standalone path: %s", stderr)
			}
			if n := accepted.Load(); n != 1 {
				t.Fatalf("create sent %d times, want exactly once", n)
			}
		})
	}
}

func TestGroupCreateOldOwnerAndMissingResult(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, mode := range []string{"old-owner", "missing-result", "owner-error", "sdk-unconfirmed"} {
		t.Run(mode, func(t *testing.T) {
			storeDir := lockedGroupCreateStore(t)
			stop, err := startSendDelegateServerForStore(t.Context(), storeDir, sendSpacing{}, func(_ context.Context, got sendDelegateRequest) (sendDelegateResponse, error) {
				switch mode {
				case "old-owner":
					// The base executable has this exact default switch rejection.
					return sendDelegateResponse{}, errors.New(`unsupported send kind "` + got.Kind + `"`)
				case "owner-error":
					return sendDelegateResponse{}, errors.New("not connected")
				case "sdk-unconfirmed":
					// What the owner's no-replay create reports when the socket
					// drops after the frame left.
					return sendDelegateResponse{}, fmt.Errorf("%w: %w", wa.ErrGroupCreateUnconfirmed, &whatsmeow.DisconnectedError{Action: "info query"})
				}
				return sendDelegateResponse{OK: true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			stdout, stderr, err := runPresenceDelegateHelper(t, append([]string{"--store", storeDir, "--json"}, groupCreateTestArgs()...))
			if err == nil || strings.Contains(stdout, `"success":true`) {
				t.Fatalf("unexpected success: %v %s %s", err, stdout, stderr)
			}
			var envelope struct {
				Error string `json:"error"`
			}
			line := strings.TrimSpace(strings.Split(strings.TrimSpace(stderr), "\n")[0])
			if err := json.Unmarshal([]byte(line), &envelope); err != nil {
				t.Fatalf("stderr is not a JSON error: %q", stderr)
			}
			switch mode {
			case "old-owner":
				// The gateway classifies this refusal by its prefix.
				if !strings.HasPrefix(envelope.Error, `unsupported send kind "group_create"`) || !strings.Contains(envelope.Error, "nothing was created") || !strings.Contains(envelope.Error, "restart `wacli sync` after upgrading") {
					t.Fatalf("old owner error = %q", envelope.Error)
				}
			case "missing-result":
				if !strings.Contains(envelope.Error, "did not report the created group; the group may exist") {
					t.Fatalf("missing result error = %q", envelope.Error)
				}
			case "owner-error":
				if envelope.Error != "not connected" {
					t.Fatalf("owner error changed: %q", envelope.Error)
				}
			case "sdk-unconfirmed":
				want := wa.ErrGroupCreateUnconfirmed.Error() + ": websocket disconnected before info query returned response"
				if envelope.Error != want {
					t.Fatalf("unconfirmed create error = %q, want %q", envelope.Error, want)
				}
			}
		})
	}
}

func TestGroupCreateHelpMarksDelegation(t *testing.T) {
	stdout, stderr, err := runPresenceDelegateHelper(t, []string{"groups", "create", "--help"})
	if err != nil || !strings.Contains(stdout, "Delegates to the running sync process when the store is locked.") {
		t.Fatalf("missing help capability marker: %v %s %s", err, stdout, stderr)
	}
}

// An OK reply that carries no group (no wrapper, an empty wrapper, or a null
// group) is an unknown outcome in both output modes: no success, no retry and
// no standalone fallback.
func TestGroupCreateReplyWithoutGroupIsUnknown(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, reply := range []string{`{"ok":true,"group_create":{}}`, `{"ok":true,"group_create":{"info":null}}`, `{"ok":true}`} {
		for _, asJSON := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/json=%t", reply, asJSON), func(t *testing.T) {
				storeDir := lockedGroupCreateStore(t)
				ln, err := delegate.Listen(storeDir)
				if err != nil {
					t.Fatal(err)
				}
				defer ln.Close()
				var accepted atomic.Int32
				go func() {
					for {
						conn, err := ln.Accept()
						if err != nil {
							return
						}
						accepted.Add(1)
						var req sendDelegateRequest
						_ = json.NewDecoder(conn).Decode(&req)
						_, _ = conn.Write([]byte(reply + "\n"))
						_ = conn.Close()
					}
				}()
				args := []string{"--store", storeDir, "--timeout", "2s"}
				if asJSON {
					args = append(args, "--json")
				}
				stdout, stderr, err := runPresenceDelegateHelper(t, append(args, groupCreateTestArgs()...))
				if err == nil {
					t.Fatalf("reply without a group reported success: stdout=%q stderr=%q", stdout, stderr)
				}
				if !strings.Contains(stderr, "did not report the created group; the group may exist, so check before creating it again") {
					t.Fatalf("stderr = %q", stderr)
				}
				output := strings.TrimSuffix(stdout, "PASS\n")
				if strings.Contains(output, `"success":true`) || strings.Contains(output, "OK") || strings.Contains(output, "JID:") {
					t.Fatalf("stdout reported a group: %q", stdout)
				}
				if strings.Contains(stderr, "store is locked") || strings.Contains(stderr, "not authenticated") {
					t.Fatalf("fell back to the standalone path: %q", stderr)
				}
				if n := accepted.Load(); n != 1 {
					t.Fatalf("create dispatched %d times, want once", n)
				}
			})
		}
	}
}

func groupCreatePhones(n int) []string {
	phones := make([]string, n)
	for i := range phones {
		phones[i] = fmt.Sprintf("+1555000%04d", i)
	}
	return phones
}

func groupCreateUserArgs(n int) []string {
	var args []string
	for _, phone := range groupCreatePhones(n) {
		args = append(args, "--user", phone)
	}
	return args
}

func TestGroupCreateParticipantsAreDistinctAndBounded(t *testing.T) {
	// One person written four ways, plus a LID twice: two participants, in
	// first-seen order.
	got, err := parseGroupCreateParticipants([]string{"+1 (234) 567-8900", "222@lid", "12345678900@s.whatsapp.net", "12345678900:3@s.whatsapp.net", "1-234-567-8900", "222@lid"})
	want := []types.JID{types.NewJID("12345678900", types.DefaultUserServer), types.NewJID("222", types.HiddenUserServer)}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("participants = %v, %v; want %v", got, err, want)
	}

	if got, err := parseGroupCreateParticipants(groupCreatePhones(maxGroupCreateParticipants)); err != nil || len(got) != 50 {
		t.Fatalf("50 distinct participants refused: %d, %v", len(got), err)
	}
	// The cap counts distinct people: 60 entries naming 50 people pass.
	if got, err := parseGroupCreateParticipants(append(groupCreatePhones(50), groupCreatePhones(10)...)); err != nil || len(got) != 50 {
		t.Fatalf("duplicates counted against the cap: %d, %v", len(got), err)
	}
	for _, users := range [][]string{nil, {}, groupCreatePhones(51)} {
		_, err := parseGroupCreateParticipants(users)
		if err == nil || !strings.HasPrefix(err.Error(), "invalid user list: ") {
			t.Fatalf("%d users -> %v", len(users), err)
		}
		if strings.Contains(err.Error(), "+1555") || strings.Contains(err.Error(), "@") {
			t.Fatalf("error carries a number: %v", err)
		}
	}

	// The owner sends the de-duplicated list, once.
	client := &fakeGroupCreateClient{info: groupCreateTestInfo()}
	req := groupCreateTestRequest()
	req.Users = append(req.Users, "12345678900@s.whatsapp.net", "222@lid")
	if _, err := executeGroupCreate(t.Context(), openGroupCreateTestDB(t), client, req); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 || len(client.req.Participants) != 2 {
		t.Fatalf("calls=%d participants=%v", client.calls, client.req.Participants)
	}
}
