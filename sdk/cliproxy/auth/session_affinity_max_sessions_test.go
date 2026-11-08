package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func newCapTestSelector(t *testing.T, cap int) *SessionAffinitySelector {
	t.Helper()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:           &RoundRobinSelector{},
		TTL:                time.Hour,
		MaxSessionsPerAuth: cap,
	})
	t.Cleanup(selector.Stop)
	return selector
}

func capTestAuths() []*Auth {
	auths := make([]*Auth, 0, 3)
	for _, id := range []string{"auth-a", "auth-b", "auth-c"} {
		auth := &Auth{ID: id, Provider: "codex", Status: StatusActive}
		auth.EnsureIndex()
		auths = append(auths, auth)
	}
	return auths
}

func pickCappedSession(t *testing.T, selector *SessionAffinitySelector, auths []*Auth, sessionID string) *Auth {
	t.Helper()
	opts := cliproxyexecutor.Options{
		Headers:  map[string][]string{"X-Claude-Code-Session-Id": {sessionID}},
		Metadata: make(map[string]any),
	}
	auth, errPick := selector.Pick(context.Background(), "codex", "gpt-5-codex", opts, auths)
	if errPick != nil {
		t.Fatalf("pick(%s): %v", sessionID, errPick)
	}
	if auth == nil {
		t.Fatalf("pick(%s) returned nil auth", sessionID)
	}
	return auth
}

func TestSessionAffinityMaxSessionsPerAuthSpreadsNewSessions(t *testing.T) {
	selector := newCapTestSelector(t, 2)
	auths := capTestAuths()

	bound := make(map[string]string, 6)
	for index := 0; index < 6; index++ {
		sessionID := fmt.Sprintf("sess-%d", index)
		auth := pickCappedSession(t, selector, auths, sessionID)
		bound[sessionID] = auth.ID
	}

	counts := make(map[string]int, len(auths))
	for _, authID := range bound {
		counts[authID]++
	}
	for _, auth := range auths {
		if counts[auth.ID] > 2 {
			t.Fatalf("credential %s hosts %d sessions, want <= 2", auth.ID, counts[auth.ID])
		}
	}
	if len(counts) != len(auths) {
		t.Fatalf("sessions spread over %d credentials, want all %d: %v", len(counts), len(auths), counts)
	}

	// Existing bindings stay put even though their credential is at the cap.
	for sessionID, authID := range bound {
		if got := pickCappedSession(t, selector, auths, sessionID); got.ID != authID {
			t.Fatalf("session %s rebound from %s to %s", sessionID, authID, got.ID)
		}
	}

	// A seventh session still gets an answer (graceful overflow instead of an error).
	if auth := pickCappedSession(t, selector, auths, "sess-overflow"); auth == nil {
		t.Fatal("overflow pick returned nil auth")
	}
}

func TestSessionAffinityMaxSessionsPerAuthDisabled(t *testing.T) {
	selector := newCapTestSelector(t, 0)
	auths := capTestAuths()
	for index := 0; index < 8; index++ {
		_ = pickCappedSession(t, selector, auths, fmt.Sprintf("sess-%d", index))
	}
	total := 0
	for _, auth := range auths {
		total += selector.cache.ActiveCount(auth.ID)
	}
	if total != 8 {
		t.Fatalf("bound %d sessions, want 8", total)
	}
}
