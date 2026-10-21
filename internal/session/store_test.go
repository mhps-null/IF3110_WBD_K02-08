package session

import (
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.IdleTimeout != 30*time.Minute {
		t.Errorf("IdleTimeout = %s", cfg.IdleTimeout)
	}
	if cfg.AbsoluteTimeout != 8*time.Hour {
		t.Errorf("AbsoluteTimeout = %s", cfg.AbsoluteTimeout)
	}
	if cfg.RotateInterval != 15*time.Minute {
		t.Errorf("RotateInterval = %s", cfg.RotateInterval)
	}
	if cfg.GracePeriod != 30*time.Second {
		t.Errorf("GracePeriod = %s", cfg.GracePeriod)
	}
}

func TestNewIDAndHashID(t *testing.T) {
	first, err := newID()
	if err != nil {
		t.Fatalf("newID() error = %v", err)
	}
	second, err := newID()
	if err != nil {
		t.Fatalf("newID() error = %v", err)
	}
	if first == second {
		t.Fatal("generated IDs must be unique")
	}
	if first == hashID(first) {
		t.Fatal("stored session identifier must be hashed")
	}
	if hashID(first) != hashID(first) {
		t.Fatal("hashID must be deterministic")
	}
}
