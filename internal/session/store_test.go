package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionID(t *testing.T) {
	first, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first == second || hashID(first) == first || hashID(first) != hashID(first) {
		t.Fatal("session ID must be random and stored as a deterministic hash")
	}
	if _, err := NewSQLStore(nil, DefaultConfig()).Load(context.Background(), "", time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty session: %v", err)
	}
}
