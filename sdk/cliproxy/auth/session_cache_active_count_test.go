package auth

import (
	"testing"
	"time"
)

func TestSessionCache_ActiveCountPerAuth(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(time.Hour)
	defer cache.Stop()

	cache.Set("s1", "auth-a")
	cache.Set("s2", "auth-a")
	cache.Set("s3", "auth-a")
	cache.Set("s4", "auth-b")

	if got := cache.ActiveCount("auth-a"); got != 3 {
		t.Fatalf("ActiveCount(auth-a) = %d, want 3", got)
	}
	if got := cache.ActiveCount("auth-b"); got != 1 {
		t.Fatalf("ActiveCount(auth-b) = %d, want 1", got)
	}

	// Aliases of one logical session count once.
	cache.SetAliases("auth-a", "s5", "s5-alias")
	if got := cache.ActiveCount("auth-a"); got != 4 {
		t.Fatalf("ActiveCount(auth-a) after alias = %d, want 4", got)
	}

	// Moving a session between credentials moves exactly one group.
	cache.Set("s1", "auth-b")
	if got := cache.ActiveCount("auth-a"); got != 3 {
		t.Fatalf("ActiveCount(auth-a) after move = %d, want 3", got)
	}
	if got := cache.ActiveCount("auth-b"); got != 2 {
		t.Fatalf("ActiveCount(auth-b) after move = %d, want 2", got)
	}

	// The same conversation using two models keeps one session count.
	cache.setSessionAliases("auth-b", "codex", "multi-model", false, "multi-model:model-a")
	cache.setSessionAliases("auth-b", "codex", "multi-model", false, "multi-model:model-b")
	if got := cache.ActiveCount("auth-b"); got != 3 {
		t.Fatalf("ActiveCount(auth-b) with model variants = %d, want 3", got)
	}

	// Subagent sessions inherit a parent credential and do not count as separate sessions.
	cache.setSessionAliases("auth-b", "codex", "parent:agent:child", true, "child-1")
	if got := cache.ActiveCount("auth-b"); got != 3 {
		t.Fatalf("ActiveCount(auth-b) with subagent = %d, want 3", got)
	}

	// Invalidating a credential drops all of its groups.
	cache.InvalidateAuth("auth-a")
	if got := cache.ActiveCount("auth-a"); got != 0 {
		t.Fatalf("ActiveCount(auth-a) after invalidate = %d, want 0", got)
	}
	if got := cache.ActiveCount("auth-b"); got != 3 {
		t.Fatalf("ActiveCount(auth-b) after invalidate = %d, want 3", got)
	}
	if got := cache.ActiveCount(""); got != 0 {
		t.Fatalf("ActiveCount(empty) = %d, want 0", got)
	}
}
