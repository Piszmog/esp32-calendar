package calendar_test

import (
	"fmt"
	"net"
	"net/http"
	"testing"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartOAuthCallback_StateMatch(t *testing.T) {
	t.Parallel()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	codeCh, errCh := calendar.StartOAuthCallback(ln, "expected-state")

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet,
		fmt.Sprintf("http://%s/callback?code=tok123&state=expected-state", ln.Addr()),
		nil,
	)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	select {
	case code := <-codeCh:
		assert.Equal(t, "tok123", code)
	case e := <-errCh:
		t.Fatalf("unexpected error: %v", e)
	case <-t.Context().Done():
		t.Fatal("timed out waiting for code")
	}
}

func TestStartOAuthCallback_StateMismatch(t *testing.T) {
	t.Parallel()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	codeCh, errCh := calendar.StartOAuthCallback(ln, "expected-state")

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet,
		fmt.Sprintf("http://%s/callback?code=tok123&state=wrong", ln.Addr()),
		nil,
	)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	select {
	case code := <-codeCh:
		t.Fatalf("expected state mismatch, got code %q", code)
	case e := <-errCh:
		require.ErrorIs(t, e, calendar.ErrStateMismatch)
	case <-t.Context().Done():
		t.Fatal("timed out waiting for error")
	}
}

func TestStartOAuthCallback_MissingState(t *testing.T) {
	t.Parallel()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	_, errCh := calendar.StartOAuthCallback(ln, "expected-state")

	// No state param at all — handler should treat it as a mismatch.
	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet,
		fmt.Sprintf("http://%s/callback?code=tok123", ln.Addr()),
		nil,
	)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	select {
	case e := <-errCh:
		require.ErrorIs(t, e, calendar.ErrStateMismatch)
	case <-t.Context().Done():
		t.Fatal("timed out waiting for error")
	}
}

func TestStartOAuthCallback_OAuthError(t *testing.T) {
	t.Parallel()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	_, errCh := calendar.StartOAuthCallback(ln, "expected-state")

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet,
		fmt.Sprintf("http://%s/callback?error=access_denied", ln.Addr()),
		nil,
	)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	select {
	case e := <-errCh:
		require.ErrorContains(t, e, "access_denied")
	case <-t.Context().Done():
		t.Fatal("timed out waiting for error")
	}
}
