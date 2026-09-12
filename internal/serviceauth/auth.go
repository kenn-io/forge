package serviceauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.kenn.io/forge/internal/tokenauth"
)

const (
	stateCookieName   = "forge_service_oauth"
	sessionCookieName = "forge_service_session"
	maxResponseBytes  = 1 << 20
	refreshEarly      = time.Minute
)

var ErrSignedOut = errors.New("service account is signed out")

type Options struct {
	ClientID         string
	ClientSecretFile string
	BaseURL          string
	DataDir          string
	OwnerID          int64
	HTTPClient       *http.Client
}

type Status struct {
	Connected bool   `json:"connected"`
	Login     string `json:"login"`
	UserID    int64  `json:"user_id"`
}

type Manager struct {
	clientID     string
	clientSecret string
	baseURL      *url.URL
	callbackURL  string
	cookiePath   string
	oauthPath    string
	ownerID      int64
	client       *http.Client
	store        *store
}

func New(opts Options) (*Manager, error) {
	if strings.TrimSpace(opts.ClientID) == "" {
		return nil, errors.New("service GitHub client ID is required")
	}
	if opts.OwnerID <= 0 {
		return nil, errors.New("service GitHub owner ID must be positive")
	}
	if strings.TrimSpace(opts.ClientSecretFile) == "" {
		return nil, errors.New("service GitHub client secret file is required")
	}
	secret, err := os.ReadFile(opts.ClientSecretFile)
	if err != nil {
		return nil, fmt.Errorf("read service GitHub client secret file: %w", err)
	}
	clientSecret := strings.TrimSpace(string(secret))
	if clientSecret == "" {
		return nil, errors.New("service GitHub client secret is empty")
	}
	baseURL, err := url.Parse(opts.BaseURL)
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" ||
		baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("service base URL must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	baseURL.Path = strings.TrimSuffix(baseURL.Path, "/")
	baseURL.RawPath = ""
	if opts.DataDir == "" {
		return nil, errors.New("service auth data directory is required")
	}
	tokenStore, err := newStore(opts.DataDir, opts.OwnerID)
	if err != nil {
		return nil, err
	}
	if _, err := tokenStore.load(); err != nil {
		return nil, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	cookiePath := baseURL.Path
	if cookiePath == "" {
		cookiePath = "/"
	}
	oauthPath := strings.TrimSuffix(cookiePath, "/") + "/auth/github"
	return &Manager{
		clientID:     strings.TrimSpace(opts.ClientID),
		clientSecret: clientSecret,
		baseURL:      baseURL,
		callbackURL:  baseURL.JoinPath("auth", "github", "callback").String(),
		cookiePath:   cookiePath,
		oauthPath:    oauthPath,
		ownerID:      opts.OwnerID,
		client:       client,
		store:        tokenStore,
	}, nil
}

func (m *Manager) StartLogin(w http.ResponseWriter, req *http.Request) {
	state, err := randomToken()
	if err != nil {
		http.Error(w, "could not start GitHub login", http.StatusInternalServerError)
		return
	}
	verifier, err := randomToken()
	if err != nil {
		http.Error(w, "could not start GitHub login", http.StatusInternalServerError)
		return
	}
	m.setCookie(w, stateCookieName, state+"."+verifier, m.oauthPath, 10*time.Minute)
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {m.clientID},
		"redirect_uri":          {m.callbackURL},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, req, "https://github.com/login/oauth/authorize?"+query.Encode(), http.StatusFound)
}

func (m *Manager) CompleteLogin(w http.ResponseWriter, req *http.Request) {
	m.clearCookie(w, stateCookieName, m.oauthPath)
	cookie, err := req.Cookie(stateCookieName)
	if err != nil {
		http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
		return
	}
	wantState, verifier, ok := strings.Cut(cookie.Value, ".")
	state := req.URL.Query().Get("state")
	code := req.URL.Query().Get("code")
	if !ok || state == "" || code == "" || !constantEqual(state, wantState) {
		http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
		return
	}
	token, err := m.exchange(req.Context(), url.Values{
		"client_id":     {m.clientID},
		"client_secret": {m.clientSecret},
		"code":          {code},
		"redirect_uri":  {m.callbackURL},
		"code_verifier": {verifier},
	})
	if err != nil {
		http.Error(w, "GitHub token exchange failed", http.StatusBadGateway)
		return
	}
	login, userID, err := m.user(req.Context(), token.AccessToken)
	if err != nil {
		http.Error(w, "GitHub user verification failed", http.StatusBadGateway)
		return
	}
	if userID != m.ownerID {
		http.Error(w, "this GitHub user does not own this Forge instance", http.StatusForbidden)
		return
	}
	now := time.Now()
	auth := &authorization{
		AccessToken:      token.AccessToken,
		RefreshToken:     token.RefreshToken,
		AccessExpiresAt:  now.Add(time.Duration(token.ExpiresIn) * time.Second),
		RefreshExpiresAt: now.Add(time.Duration(token.RefreshExpiresIn) * time.Second),
		Login:            login,
		UserID:           userID,
	}
	err = m.store.locked(req.Context(), func() error {
		existing, loadErr := m.store.load()
		if loadErr != nil {
			return loadErr
		}
		if existing != nil {
			auth.BrowserToken = existing.BrowserToken
		} else {
			auth.BrowserToken, loadErr = randomToken()
			if loadErr != nil {
				return loadErr
			}
		}
		return m.store.save(auth)
	})
	if err != nil {
		http.Error(w, "could not persist GitHub authorization", http.StatusInternalServerError)
		return
	}
	m.setCookie(w, sessionCookieName, auth.BrowserToken, m.cookiePath, 0)
	http.Redirect(w, req, m.baseURL.String(), http.StatusSeeOther)
}

func (m *Manager) Logout(w http.ResponseWriter, req *http.Request) {
	if err := m.store.locked(req.Context(), m.store.remove); err != nil {
		http.Error(w, "could not disconnect GitHub authorization", http.StatusInternalServerError)
		return
	}
	m.clearCookie(w, sessionCookieName, m.cookiePath)
	http.Redirect(w, req, m.baseURL.String(), http.StatusSeeOther)
}

func (m *Manager) Authenticated(req *http.Request) bool {
	cookie, err := req.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	auth, err := m.store.load()
	return err == nil && auth != nil && auth.usable(time.Now()) && constantEqual(cookie.Value, auth.BrowserToken)
}

func (m *Manager) Token(ctx context.Context) (string, error) {
	var result string
	err := m.store.locked(ctx, func() error {
		auth, err := m.store.load()
		if err != nil {
			return err
		}
		if auth == nil {
			return ErrSignedOut
		}
		now := time.Now()
		if auth.AccessExpiresAt.After(now.Add(refreshEarly)) {
			result = auth.AccessToken
			return nil
		}
		if !auth.RefreshExpiresAt.After(now) {
			if err := m.store.remove(); err != nil {
				return errors.Join(ErrSignedOut, err)
			}
			return ErrSignedOut
		}
		refreshed, terminal, err := m.refresh(ctx, auth.RefreshToken)
		if err != nil {
			if terminal {
				if removeErr := m.store.remove(); removeErr != nil {
					return errors.Join(ErrSignedOut, removeErr)
				}
				return ErrSignedOut
			}
			return err
		}
		auth.AccessToken = refreshed.AccessToken
		auth.RefreshToken = refreshed.RefreshToken
		auth.AccessExpiresAt = now.Add(time.Duration(refreshed.ExpiresIn) * time.Second)
		auth.RefreshExpiresAt = now.Add(time.Duration(refreshed.RefreshExpiresIn) * time.Second)
		if err := m.store.save(auth); err != nil {
			return err
		}
		result = auth.AccessToken
		return nil
	})
	return result, err
}

func (m *Manager) Invalidate(token string) {
	if token == "" {
		return
	}
	_ = m.store.locked(context.Background(), func() error {
		auth, err := m.store.load()
		if err != nil || auth == nil || !constantEqual(token, auth.AccessToken) {
			return err
		}
		auth.AccessExpiresAt = time.Unix(1, 0)
		return m.store.save(auth)
	})
}

func (m *Manager) Status() Status {
	auth, err := m.store.load()
	if err != nil || auth == nil || !auth.usable(time.Now()) {
		return Status{}
	}
	return Status{Connected: true, Login: auth.Login, UserID: auth.UserID}
}

func (m *Manager) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "github", Host: "github.com"},
		Candidates: []tokenauth.Candidate{{
			Kind: tokenauth.SourceKindGitHubAppUser,
			Host: "github.com",
		}},
	}
}

type oauthToken struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_token_expires_in"`
	TokenType        string `json:"token_type"`
}

func (t *oauthToken) valid() bool {
	return t.AccessToken != "" && t.RefreshToken != "" && t.ExpiresIn > 0 &&
		t.RefreshExpiresIn > 0 && strings.EqualFold(t.TokenType, "bearer")
}

func (m *Manager) exchange(ctx context.Context, values url.Values) (*oauthToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(values.Encode()))
	if err != nil {
		return nil, errors.New("create GitHub token request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := m.client.Do(req)
	if err != nil {
		return nil, errors.New("send GitHub token request")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, errors.New("GitHub token endpoint rejected request")
	}
	var token oauthToken
	if err := decodeResponse(response.Body, &token); err != nil || !token.valid() {
		return nil, errors.New("GitHub token endpoint returned a malformed response")
	}
	return &token, nil
}

func (m *Manager) refresh(ctx context.Context, refreshToken string) (*oauthToken, bool, error) {
	values := url.Values{
		"client_id":     {m.clientID},
		"client_secret": {m.clientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(values.Encode()))
	if err != nil {
		return nil, false, errors.New("create GitHub refresh request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := m.client.Do(req)
	if err != nil {
		return nil, false, errors.New("send GitHub refresh request")
	}
	var result struct {
		oauthToken
		Error string `json:"error"`
	}
	if err := decodeResponse(response.Body, &result); err != nil {
		return nil, false, errors.New("GitHub refresh endpoint returned a malformed response")
	}
	if result.Error != "" || response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, result.Error == "bad_refresh_token", errors.New("GitHub refresh request was rejected")
	}
	if !result.valid() {
		return nil, false, errors.New("GitHub refresh endpoint returned a malformed response")
	}
	return &result.oauthToken, false, nil
}

func (m *Manager) user(ctx context.Context, accessToken string) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", 0, errors.New("create GitHub user request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := m.client.Do(req)
	if err != nil {
		return "", 0, errors.New("send GitHub user request")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return "", 0, errors.New("GitHub user endpoint rejected request")
	}
	var user struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := decodeResponse(response.Body, &user); err != nil || user.Login == "" || user.ID <= 0 {
		return "", 0, errors.New("GitHub user endpoint returned a malformed response")
	}
	return user.Login, user.ID, nil
}

func decodeResponse(body io.ReadCloser, destination any) error {
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return errors.New("response body could not be read")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(destination); err != nil {
		return errors.New("response body is malformed")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("response body has trailing data")
	}
	return nil
}

func randomToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func constantEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func (m *Manager) setCookie(w http.ResponseWriter, name, value, path string, lifetime time.Duration) {
	cookie := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if lifetime > 0 {
		cookie.MaxAge = int(lifetime.Seconds())
		cookie.Expires = time.Now().Add(lifetime)
	}
	http.SetCookie(w, cookie)
}

func (m *Manager) clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Path:     path,
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
