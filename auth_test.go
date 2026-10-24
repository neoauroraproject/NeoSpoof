package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, password, created, err := openAuth(dir, false)
	if err != nil || !created || password == "" {
		t.Fatalf("create: %v %q created=%v", err, password, created)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), password) {
		t.Fatal("password stored in plaintext")
	}
	if _, err := store.check("wrong-password"); err == nil {
		t.Fatal("accepted a wrong password")
	}
	if _, err := store.check(password); err != nil {
		t.Fatal(err)
	}
	next := "replacement-password"
	if _, err := store.change(password, next); err != nil {
		t.Fatal(err)
	}
	if _, err := store.check(password); err == nil {
		t.Fatal("old password still works")
	}
	if _, err := store.check(next); err != nil {
		t.Fatal(err)
	}
	again, _, created, err := openAuth(dir, false)
	if err != nil || created {
		t.Fatalf("reopen created=%v err=%v", created, err)
	}
	if _, err := again.check(next); err != nil {
		t.Fatal(err)
	}
}
