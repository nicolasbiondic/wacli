package main

import (
	"database/sql"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
)

// These tests only use symbols that exist before label writes, so they also
// compile on the base and fail there on the missing commands. The fake owner
// speaks the raw .send.sock JSON protocol of a running `sync --follow`.
func startRawAppStateOwner(t *testing.T, storeDir string) <-chan map[string]any {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(storeDir, sendDelegateSocketName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	requests := make(chan map[string]any, 16)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req map[string]any
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				conn.Close()
				continue
			}
			requests <- req
			payload, _ := req["app_state"].(map[string]any)
			action, _ := payload["action"].(string)
			result := map[string]any{"action": action}
			switch {
			case strings.HasPrefix(action, "quick_reply"):
				result["quick_reply"] = map[string]any{"id": "1700000000", "shortcut": "prueba", "message": "m", "keywords": []string{}, "label_ids": []string{}}
			default:
				result["label"] = map[string]any{"id": "10", "name": "Prueba Vendas"}
			}
			if strings.HasPrefix(action, "chat_label") {
				result["chat"] = "123@s.whatsapp.net"
			}
			_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true, "action": action, "app_state": result})
			conn.Close()
		}
	}()
	return requests
}

func TestLabelAndQuickReplyCommandsDelegateToSyncOwner(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	requests := startRawAppStateOwner(t, storeDir)

	tests := []struct {
		args    []string
		payload map[string]any
		stdout  string
	}{
		{[]string{"lists", "create", "--name", "Prueba Vendas", "--color", "3"},
			map[string]any{"action": "label_create", "name": "Prueba Vendas", "color": 3.0}, `"label":{"id":"10"`},
		{[]string{"labels", "create", "--name", "Prueba Vendas", "--id", "40"},
			map[string]any{"action": "label_create", "name": "Prueba Vendas", "id": "40"}, `"action":"label_create"`},
		{[]string{"labels", "edit", "Prueba Vendas", "--color", "0"},
			map[string]any{"action": "label_edit", "ref": "Prueba Vendas", "color": 0.0}, `"action":"label_edit"`},
		{[]string{"lists", "delete", "10"},
			map[string]any{"action": "label_delete", "ref": "10"}, `"action":"label_delete"`},
		{[]string{"chats", "label", "add", "--chat", "123@s.whatsapp.net", "--label", "Prueba Vendas"},
			map[string]any{"action": "chat_label_add", "ref": "Prueba Vendas", "chat": "123@s.whatsapp.net"}, `"chat":"123@s.whatsapp.net"`},
		{[]string{"chats", "label", "remove", "--chat", "Fixture", "--pick", "2", "--label", "10"},
			map[string]any{"action": "chat_label_remove", "ref": "10", "chat": "Fixture", "pick": 2.0}, `"action":"chat_label_remove"`},
		{[]string{"quick-replies", "create", "--shortcut", "prueba", "--message", "Mensaje", "--keyword", "a", "--keyword", "b c"},
			map[string]any{"action": "quick_reply_create", "shortcut": "prueba", "message": "Mensaje", "keywords": []any{"a", "b c"}}, `"quick_reply":{`},
		{[]string{"quick-replies", "edit", "/prueba", "--clear-keywords"},
			map[string]any{"action": "quick_reply_edit", "ref": "/prueba", "keywords": []any{}}, `"action":"quick_reply_edit"`},
		{[]string{"quick-replies", "delete", "1700000000"},
			map[string]any{"action": "quick_reply_delete", "ref": "1700000000"}, `"action":"quick_reply_delete"`},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args[:2], " "), func(t *testing.T) {
			args := append([]string{"--store", storeDir, "--json", "--timeout", "5s"}, tt.args...)
			stdout, stderr, err := runPresenceDelegateHelper(t, args)
			if err != nil {
				t.Fatalf("%v: %v stdout=%q stderr=%q", tt.args, err, stdout, stderr)
			}
			var req map[string]any
			select {
			case req = <-requests:
			case <-contextWithTestTimeout(t).Done():
				t.Fatal("no delegated request")
			}
			if req["kind"] != "app_state_write" || !reflect.DeepEqual(req["app_state"], tt.payload) {
				t.Fatalf("request = %v, want app_state %v", req, tt.payload)
			}
			if !strings.Contains(stdout, `"ok":true`) || !strings.Contains(stdout, tt.stdout) {
				t.Fatalf("stdout %q missing %s", stdout, tt.stdout)
			}
		})
	}
}

func TestLabelAndQuickReplyCommandsValidateBeforeOpeningStore(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"lists", "create"}, "--name is required"},
		{[]string{"lists", "edit", "7"}, "nothing to change"},
		{[]string{"chats", "label", "add", "--label", "7"}, "--chat is required"},
		{[]string{"chats", "label", "remove", "--chat", "123"}, "--label is required"},
		{[]string{"quick-replies", "create", "--shortcut", "x"}, "--shortcut and --message are required"},
		{[]string{"quick-replies", "edit", "x"}, "nothing to change"},
		{[]string{"quick-replies", "edit", "x", "--keyword", "a", "--clear-keywords"}, "mutually exclusive"},
		{[]string{"--read-only", "lists", "create", "--name", "x"}, "read-only"},
		{[]string{"--read-only", "quick-replies", "delete", "x"}, "read-only"},
	} {
		dir := t.TempDir()
		var err error
		captureRootStderr(t, func() { err = execute(append([]string{"--store", dir}, tc.args...)) })
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "session.db")); !os.IsNotExist(statErr) {
			t.Fatalf("%v created a WhatsApp session: %v", tc.args, statErr)
		}
	}
}

func TestQuickRepliesListCLIReadOnlyJSON(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	seed, err := sql.Open("sqlite3", filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = seed.Exec(`INSERT INTO quick_replies(id,shortcut,message,keywords,label_ids,count,deleted,updated_at)
		VALUES('1','hola','Hola','["k"]','[]',2,0,100),('2','gone','x','[]','[]',NULL,1,100)`)
	seed.Close()
	if err != nil {
		t.Fatal(err)
	}
	raw := captureRootStdout(t, func() {
		if err := execute([]string{"--store", dir, "--read-only", "--json", "quick-replies", "list"}); err != nil {
			t.Fatal(err)
		}
	})
	var result struct {
		Success bool `json:"success"`
		Data    struct {
			QuickReplies []map[string]any `json:"quick_replies"`
			Completeness string           `json:"completeness"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Data.Completeness != "unknown" || len(result.Data.QuickReplies) != 1 ||
		result.Data.QuickReplies[0]["shortcut"] != "hola" || !reflect.DeepEqual(result.Data.QuickReplies[0]["keywords"], []any{"k"}) {
		t.Fatalf("quick-replies list = %s", raw)
	}
}
