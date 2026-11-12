package main

import (
	"crypto/sha256"
	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewDeltaLabelsReadOnlySafety(t *testing.T) {
	dir := t.TempDir()
	a, err := app.New(app.Options{StoreDir: dir, AllowUnauthed: true})
	if err != nil {
		t.Fatal(err)
	}
	name := "synthetic-label"
	if err := a.DB().UpsertList(store.ListUpdate{ID: "7", Name: &name}); err != nil {
		t.Fatal(err)
	}
	chat := "15550000001@s.whatsapp.net"
	if err := a.DB().UpsertChat(chat, "dm", "synthetic", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := a.DB().SetChatList(chat, "7", true, time.Unix(1, 0), false); err != nil {
		t.Fatal(err)
	}
	a.Close()
	path := filepath.Join(dir, "wacli.db")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"lists", "list"}, {"chats", "list", "--list", "7"}} {
		captureRootStdout(t, func() {
			if err := execute(append([]string{"--store", dir, "--read-only", "--json"}, args...)); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, args := range [][]string{{"app-state", "resync", "regular"}, {"lists", "edit", "7", "--name", "changed"}} {
		err := execute(append([]string{"--store", dir, "--read-only", "--json"}, args...))
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatal("read-only write command was not rejected")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("read-only label commands changed database bytes")
	}
	if _, err := os.Stat(filepath.Join(dir, "session.db")); !os.IsNotExist(err) {
		t.Fatal("label reads unexpectedly created a session database")
	}
	t.Log("label reads retained database bytes, created no session, and refused writes/resync")
}
