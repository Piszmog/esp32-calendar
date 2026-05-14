package calendar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gcalendar "google.golang.org/api/calendar/v3"
)

const tokenFileMode = os.FileMode(0600)

var (
	errOAuthFailed   = errors.New("oauth callback returned error")
	errNoCode        = errors.New("oauth callback missing code")
	errStateMismatch = errors.New("oauth callback state mismatch")
	errTokenPerms    = errors.New("token file has insecure permissions")
)

// loadOAuthConfig reads credentials.json and returns an OAuth2 Config.
// redirectAddr should be the loopback URL (e.g. "http://127.0.0.1:8090/callback")
// that you've configured in the Google Cloud Console OAuth client. Pass ""
// when only using the config for refresh-token-driven calls (no redirect needed).
func loadOAuthConfig(credsPath, redirectAddr string) (*oauth2.Config, error) {
	b, err := os.ReadFile(credsPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", credsPath, err)
	}
	cfg, err := google.ConfigFromJSON(b, gcalendar.CalendarReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if redirectAddr != "" {
		cfg.RedirectURL = redirectAddr
	}
	return cfg, nil
}

// RunAuth performs the full OAuth dance:
//  1. Listen on a loopback port.
//  2. Print the auth URL; user opens it in their browser (typically via SSH tunnel).
//  3. Google redirects back to our loopback URL with ?code=...
//  4. Exchange code for token; persist it.
func RunAuth(c Config) error {
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", c.AuthListenPort)
	oc, err := loadOAuthConfig(c.CredentialsPath, redirect)
	if err != nil {
		return err
	}

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", c.AuthListenPort))
	if err != nil {
		return fmt.Errorf("listen on %s: %w", redirect, err)
	}
	defer func() { _ = ln.Close() }()

	state, err := newOAuthState()
	if err != nil {
		return fmt.Errorf("generate oauth state: %w", err)
	}
	authURL := oc.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	printOAuthInstructions(redirect, authURL, c.AuthListenPort)

	codeCh, errCh := startOAuthCallback(ln, state)

	var code string
	select {
	case code = <-codeCh:
	case err = <-errCh:
		return err
	}

	tok, err := oc.Exchange(context.Background(), code)
	if err != nil {
		return fmt.Errorf("exchange code: %w", err)
	}
	return saveToken(c.TokenPath, tok)
}

func newOAuthState() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// printOAuthInstructions writes the auth setup steps to stdout.
func printOAuthInstructions(redirect, authURL string, port int) {
	_, _ = fmt.Fprintln(os.Stdout)
	_, _ = fmt.Fprintln(os.Stdout, "=== OAuth setup ===")
	_, _ = fmt.Fprintln(os.Stdout, "1. Make sure you've added this exact redirect URI to your OAuth client:")
	_, _ = fmt.Fprintln(os.Stdout, "     ", redirect)
	_, _ = fmt.Fprintln(os.Stdout)
	_, _ = fmt.Fprintln(os.Stdout, "2. If running on a headless Pi, set up SSH port forwarding from your laptop:")
	_, _ = fmt.Fprintf(os.Stdout, "     ssh -L %d:127.0.0.1:%d server@esp32-calendar.local\n", port, port)
	_, _ = fmt.Fprintln(os.Stdout)
	_, _ = fmt.Fprintln(os.Stdout, "3. Open this URL in your browser (on your laptop):")
	_, _ = fmt.Fprintln(os.Stdout, "     ", authURL)
	_, _ = fmt.Fprintln(os.Stdout)
	_, _ = fmt.Fprintln(os.Stdout, "Waiting for Google to redirect back...")
}

// startOAuthCallback registers the /callback handler on ln and starts serving.
// Returns a code channel (one value on success) and an error channel (one value on failure).
// state must match the value passed to AuthCodeURL; a mismatch rejects the callback.
func startOAuthCallback(ln net.Listener, state string) (<-chan string, <-chan error) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", oauthCallbackHandler(state, codeCh, errCh))

	go func() {
		if err := http.Serve(ln, mux); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Error("oauth http server", "err", err)
		}
	}()

	return codeCh, errCh
}

// oauthCallbackHandler returns the HTTP handler for the OAuth callback endpoint.
// Sends are non-blocking so a browser retry (second request) does not block.
func oauthCallbackHandler(state string, codeCh chan<- string, errCh chan<- error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if errStr := r.URL.Query().Get("error"); errStr != "" {
			_, _ = fmt.Fprintf(w, "OAuth error: %s", errStr)
			trySendErr(errCh, fmt.Errorf("%w: %s", errOAuthFailed, errStr))
			return
		}
		if got := r.URL.Query().Get("state"); got != state {
			_, _ = fmt.Fprint(w, "state mismatch")
			trySendErr(errCh, errStateMismatch)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			_, _ = fmt.Fprint(w, "no code in response")
			trySendErr(errCh, errNoCode)
			return
		}
		_, _ = fmt.Fprint(w, "Got it. You can close this window and return to the terminal.")
		trySendStr(codeCh, code)
	}
}

func trySendErr(ch chan<- error, err error) {
	select {
	case ch <- err:
	default:
	}
}

func trySendStr(ch chan<- string, s string) {
	select {
	case ch <- s:
	default:
	}
}

func saveToken(path string, t *oauth2.Token) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, tokenFileMode)
	if err != nil {
		return fmt.Errorf("create token file: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := json.NewEncoder(f).Encode(t); err != nil {
		return fmt.Errorf("encode token: %w", err)
	}
	return nil
}

func loadToken(path string) (*oauth2.Token, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open token: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat token file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w (mode %04o): run: chmod 600 %s", errTokenPerms, info.Mode().Perm(), path)
	}
	var t oauth2.Token
	if err := json.NewDecoder(f).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}
	return &t, nil
}

// tokenSource returns a refreshing TokenSource for use with API calls.
// The library auto-refreshes when the access token is near expiry.
func tokenSource(ctx context.Context, c Config) (oauth2.TokenSource, error) {
	oc, err := loadOAuthConfig(c.CredentialsPath, "")
	if err != nil {
		return nil, err
	}
	tok, err := loadToken(c.TokenPath)
	if err != nil {
		return nil, fmt.Errorf("read token (run with -auth first?): %w", err)
	}
	return oc.TokenSource(ctx, tok), nil
}
