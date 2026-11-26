package googleio

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/gmail/v1"
	identity "google.golang.org/api/oauth2/v2"
	"google.golang.org/api/option"
)

const callbackHost = "127.0.0.1:43891"
const callbackPath = "/callback"

type credentials struct {
	Service  string       `json:"service"`
	ClientID string       `json:"client_id"`
	Email    string       `json:"email"`
	Token    oauth2.Token `json:"token"`
}

func oauthConfig(c Config, service string) (*oauth2.Config, error) {
	if err := checkService(service); err != nil {
		return nil, err
	}
	if c.SecretsDir == "" {
		return nil, errors.New("set CODEX_SECRETS_DIR")
	}
	body, err := os.ReadFile(filepath.Join(c.SecretsDir, "google-client.json"))
	if err != nil {
		return nil, errors.New("cannot read google-client.json in CODEX_SECRETS_DIR")
	}
	var doc struct {
		Installed json.RawMessage `json:"installed"`
	}
	if json.Unmarshal(body, &doc) != nil || len(doc.Installed) == 0 {
		return nil, errors.New("use a Google Desktop application OAuth client in google-client.json")
	}
	scopes := []string{drive.DriveReadonlyScope}
	if service == "gmail" {
		scopes = []string{gmail.GmailSendScope, identity.UserinfoEmailScope}
	}
	cfg, err := google.ConfigFromJSON(body, scopes...)
	if err != nil || cfg.ClientID == "" {
		return nil, errors.New("invalid OAuth desktop client configuration")
	}
	cfg.Endpoint = google.Endpoint
	cfg.RedirectURL = "http://" + callbackHost + callbackPath
	return cfg, nil
}

func tokenPath(c Config, service string) string {
	return filepath.Join(c.SecretsDir, service+"-token.json")
}

// Write the entire credential atomically. A failed consent or interrupted
// refresh must leave the previously authorized account usable.
func saveCredentials(path string, creds credentials) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".oauth-*")
	if err != nil {
		return errors.New("cannot create private OAuth token file")
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(&creds)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil || os.Rename(f.Name(), path) != nil {
		return errors.New("cannot persist private OAuth token")
	}
	return nil
}

type refreshFailure struct{ message string }

func (e *refreshFailure) Error() string { return e.message }

// Both offline refresh and interactive reauthorization can update the tokens.
// Serialize read/refresh/write so concurrent runs cannot restore an old grant.
func withTokenLock(path string, work func() error) error {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return errors.New("cannot open Google credential lock")
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return errors.New("cannot lock Google credentials")
	}
	return work()
}

func readCredentials(path, service, clientID string) (credentials, error) {
	b, err := os.ReadFile(path)
	var rec credentials
	if err != nil || json.Unmarshal(b, &rec) != nil || rec.Token.RefreshToken == "" || rec.Service != service || rec.ClientID != clientID {
		return credentials{}, fmt.Errorf("authorize %s using the configured OAuth client", service)
	}
	return rec, nil
}

type tokenCache struct {
	ctx    context.Context
	cfg    *oauth2.Config
	path   string
	record credentials
}

func (s *tokenCache) Token() (*oauth2.Token, error) {
	var t *oauth2.Token
	err := withTokenLock(s.path, func() error {
		rec, err := readCredentials(s.path, s.record.Service, s.cfg.ClientID)
		if err != nil || !strings.EqualFold(rec.Email, s.record.Email) {
			return errors.New("Google authorization changed. Restart the task with the intended account")
		}
		if rec.Token.Valid() {
			t = &rec.Token
			return nil
		}
		t, err = s.cfg.TokenSource(s.ctx, &rec.Token).Token()
		if err != nil {
			return errors.New("Google token refresh failed. Check credentials and reauthorize if revoked")
		}
		if t.RefreshToken == "" {
			t.RefreshToken = rec.Token.RefreshToken
		}
		rec.Token = *t
		return saveCredentials(s.path, rec)
	})
	if err != nil {
		return nil, &refreshFailure{err.Error()}
	}
	return t, nil
}

func authorizedClient(ctx context.Context, c Config, service string) (*http.Client, error) {
	cfg, err := oauthConfig(c, service)
	if err != nil {
		return nil, err
	}
	path := tokenPath(c, service)
	record, err := readCredentials(path, service, cfg.ClientID)
	if err != nil {
		return nil, err
	}
	if service == "gmail" && !strings.EqualFold(record.Email, c.Sender) {
		return nil, errors.New("Gmail authorization belongs to a different sender; authorize the configured account")
	}
	refreshCtx := context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: 30 * time.Second})
	cache := &tokenCache{ctx: refreshCtx, cfg: cfg, path: path, record: record}
	client := oauth2.NewClient(ctx, oauth2.ReuseTokenSource(&record.Token, cache))
	client.Timeout = 3 * time.Minute
	return client, nil
}

// Authorize prompts on stderr so normal command JSON remains on stdout. When
// administering a remote server, forward callbackHost to the server using SSH.
func Authorize(ctx context.Context, c Config, service string) error {
	cfg, err := oauthConfig(c, service)
	if err != nil {
		return err
	}
	if service == "gmail" && !validAddress(c.Sender) {
		return errors.New("configure the intended Gmail sender before authorization")
	}
	listener, err := net.Listen("tcp4", callbackHost)
	if err != nil {
		return fmt.Errorf("cannot bind OAuth callback at %s", callbackHost)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	state := base64.RawURLEncoding.EncodeToString(nonce[:])
	verifier := oauth2.GenerateVerifier()
	codes := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+callbackPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Host != callbackHost || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid OAuth callback.", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" && r.URL.Query().Get("error") == "" {
			http.Error(w, "Missing authorization code.", http.StatusBadRequest)
			return
		}
		select {
		case codes <- code:
		default:
		}
		fmt.Fprintln(w, "Authorization response received. Return to the terminal.")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(listener) }()
	options := []oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent select_account"), oauth2.S256ChallengeOption(verifier)}
	if service == "gmail" {
		options = append(options, oauth2.SetAuthURLParam("login_hint", c.Sender))
	}
	fmt.Fprintln(os.Stderr, "Open this Google consent URL in your browser (remote server: forward local port 43891 over SSH):")
	fmt.Fprintln(os.Stderr, cfg.AuthCodeURL(state, options...))
	var code string
	select {
	case code = <-codes:
	case <-ctx.Done():
		return errors.New("Google authorization canceled or timed out")
	case <-serverErrors:
		return errors.New("Google authorization callback stopped")
	}
	if code == "" {
		return errors.New("Google authorization declined")
	}
	token, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil || token.RefreshToken == "" {
		return errors.New("OAuth exchange did not provide an offline token; repeat consent")
	}
	if scope, ok := token.Extra("scope").(string); ok {
		granted := strings.Fields(scope)
		for _, required := range cfg.Scopes {
			if !slices.Contains(granted, required) {
				return errors.New("Google did not grant all requested permissions")
			}
		}
	}
	transport := cfg.Client(ctx, token)
	email, err := accountEmail(ctx, service, transport)
	if err != nil {
		return err
	}
	if service == "gmail" && !strings.EqualFold(email, c.Sender) {
		return errors.New("authorized Google identity differs from the configured Gmail sender")
	}
	path := tokenPath(c, service)
	return withTokenLock(path, func() error {
		return saveCredentials(path, credentials{Service: service, ClientID: cfg.ClientID, Email: email, Token: *token})
	})
}

func accountEmail(ctx context.Context, service string, transport *http.Client) (string, error) {
	if service == "drive" {
		client, err := drive.NewService(ctx, option.WithHTTPClient(transport))
		if err != nil {
			return "", errors.New("cannot initialize Drive client")
		}
		about, err := client.About.Get().Fields("user(emailAddress)").Context(ctx).Do()
		if err != nil || about.User == nil || about.User.EmailAddress == "" {
			return "", errors.New("could not verify the authorized Drive account")
		}
		return about.User.EmailAddress, nil
	}
	client, err := identity.NewService(ctx, option.WithHTTPClient(transport))
	if err != nil {
		return "", errors.New("cannot initialize Google identity client")
	}
	info, err := client.Userinfo.Get().Context(ctx).Do()
	if err != nil || info.VerifiedEmail == nil || !*info.VerifiedEmail || !validAddress(info.Email) {
		return "", errors.New("could not verify the Google sender identity")
	}
	return info.Email, nil
}
