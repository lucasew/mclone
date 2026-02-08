package geminioauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/providers/gemini"
	"github.com/lucasew/mclone/pkg/remote"
	"google.golang.org/genai"
)

const (
	clientId     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	clientSecret = "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"
	redirectPort = "8085"
	redirectPath = "/oauth2callback"
	redirectURI  = "http://localhost:" + redirectPort + redirectPath
	authURL      = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenURL     = "https://oauth2.googleapis.com/token"
)

var scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

type GeminiOAuthProvider struct {
	base     *gemini.GeminiProvider
	name     string
	options  map[string]string
	resolver remote.Resolver
	token    *TokenData
}

func (p *GeminiOAuthProvider) Name() string { return "geminioauth" }

func (p *GeminiOAuthProvider) List(ctx context.Context) ([]remote.Model, error) {
	// ensureToken is called inside the client factory, but List calls client(), so it's covered.
	return p.base.List(ctx)
}

func (p *GeminiOAuthProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	return p.base.Chat(ctx, modelName, messages, options)
}

// clientFactory injects the OAuth token
func (p *GeminiOAuthProvider) clientFactory(ctx context.Context) (*genai.Client, error) {
	if err := p.ensureToken(ctx); err != nil {
		return nil, err
	}

	// p.base.APIKey holds the Access Token now (set by ensureToken)
	accessToken := p.base.APIKey

	transport := &oauthTransport{
		accessToken: accessToken,
		base:        http.DefaultTransport,
	}

	httpClient := &http.Client{
		Transport: transport,
	}

	return genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     "", // Intentionally empty to rely on Authorization header
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: httpClient,
	})
}

type oauthTransport struct {
	accessToken string
	base        http.RoundTripper
}

func (t *oauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.Header.Set("Authorization", "Bearer "+t.accessToken)
	// Ensure no API key query param interferes
	q := newReq.URL.Query()
	q.Del("key")
	newReq.URL.RawQuery = q.Encode()
	return t.base.RoundTrip(newReq)
}

// Token management

type TokenData struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"` // seconds
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`
}

func (p *GeminiOAuthProvider) ensureToken(ctx context.Context) error {
	token, err := p.loadToken()
	if err == nil && token != nil {
		if time.Now().Add(time.Minute).Before(token.ExpiresAt) {
			p.base.APIKey = token.AccessToken
			return nil
		}
		// Refresh
		if token.RefreshToken != "" {
			slog.Info("gemini_oauth_refreshing_token")
			newToken, err := p.refreshToken(token.RefreshToken)
			if err == nil {
				p.saveToken(newToken)
				p.base.APIKey = newToken.AccessToken
				return nil
			}
			slog.Warn("gemini_oauth_refresh_failed", "error", err)
		}
	}

	// Login
	slog.Info("gemini_oauth_login_required")
	newToken, err := p.performLoginFlow(ctx)
	if err != nil {
		return err
	}
	p.saveToken(newToken)
	p.base.APIKey = newToken.AccessToken
	return nil
}

func (p *GeminiOAuthProvider) loadToken() (*TokenData, error) {
	if p.token != nil {
		return p.token, nil
	}

	// Load from options
	tokenJSON := p.options["token"]
	if tokenJSON == "" {
		return nil, fmt.Errorf("no token found in config")
	}

	var token TokenData
	if err := json.Unmarshal([]byte(tokenJSON), &token); err != nil {
		return nil, fmt.Errorf("failed to parse token from config: %w", err)
	}
	p.token = &token
	return &token, nil
}

func (p *GeminiOAuthProvider) saveToken(token *TokenData) error {
	p.token = token
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}

	if p.resolver.UpdateOptions != nil {
		updates := map[string]string{
			"token": string(data),
		}
		return p.resolver.UpdateOptions(p.name, updates)
	}
	return nil
}

func (p *GeminiOAuthProvider) refreshToken(refreshToken string) (*TokenData, error) {
	data := url.Values{}
	data.Set("client_id", clientId)
	data.Set("client_secret", clientSecret)
	data.Set("refresh_token", refreshToken)
	data.Set("grant_type", "refresh_token")

	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("refresh failed status %d: %s", resp.StatusCode, string(body))
	}

	var newToken TokenData
	if err := json.NewDecoder(resp.Body).Decode(&newToken); err != nil {
		return nil, err
	}
	newToken.RefreshToken = refreshToken // Keep old refresh token if new one not provided (usually it is NOT provided on refresh unless rotated)
	// Check if new refresh token was actually provided in response (Google might rotate)
	// Note: json.Decode maps fields. If refresh_token is in JSON, it overwrites. If not, it stays empty.
	// We should check raw map if we want to be sure, but struct decode is fine if we re-assign if empty.
	// Actually, wait: we unmarshaled into a clean struct. So if response has no refresh_token, it is empty.
	// Google Refresh responses usually do NOT return a new refresh token unless configured.

	// Re-read to check
	// Simpler: decode into map first? No, just logic:
	// If newToken.RefreshToken is empty, use old one.

	// BUT, I need to check if the field was present. `newToken.RefreshToken` will be "" if not present.
	// But it might also be "" if present and empty (unlikely).
	// Let's assume "" means not present.

	// Ah wait, I need to handle `ExpiresAt`. `ExpiresIn` comes from API.
	newToken.ExpiresAt = time.Now().Add(time.Duration(newToken.ExpiresIn) * time.Second)

	// Fix refresh token preservation
	if newToken.RefreshToken == "" {
		newToken.RefreshToken = refreshToken
	}

	return &newToken, nil
}

// PKCE Flow

func (p *GeminiOAuthProvider) performLoginFlow(ctx context.Context) (*TokenData, error) {
	// 1. PKCE Generation
	verifier, challenge := generatePKCE()
	state := generateRandomString(16)

	// 2. Start Callback Server
	codeCh := make(chan string)
	errCh := make(chan error)

	srv := &http.Server{Addr: ":" + redirectPort}
	http.HandleFunc(redirectPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "Invalid state", http.StatusBadRequest)
			errCh <- fmt.Errorf("invalid state")
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "No code", http.StatusBadRequest)
			errCh <- fmt.Errorf("no code")
			return
		}
		w.Write([]byte("Login successful! You can close this window."))
		codeCh <- code
	})

	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	defer srv.Shutdown(context.Background())

	// 3. Open Browser
	u, _ := url.Parse(authURL)
	q := u.Query()
	q.Set("client_id", clientId)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("access_type", "offline") // Important for refresh token
	q.Set("prompt", "consent")
	u.RawQuery = q.Encode()

	fmt.Printf("Opening browser to authenticate: %s\n", u.String())
	openBrowser(u.String())

	// 4. Wait for Code
	select {
	case code := <-codeCh:
		// 5. Exchange Code
		return p.exchangeCode(code, verifier)
	case err := <-errCh:
		return nil, err
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("timeout waiting for login")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *GeminiOAuthProvider) exchangeCode(code, verifier string) (*TokenData, error) {
	data := url.Values{}
	data.Set("client_id", clientId)
	data.Set("client_secret", clientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("grant_type", "authorization_code")
	data.Set("code_verifier", verifier)

	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("exchange failed status %d: %s", resp.StatusCode, string(body))
	}

	var token TokenData
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, err
	}
	token.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	return &token, nil
}

// Helpers

func generatePKCE() (verifier, challenge string) {
	// Verifier: random 32-byte string, base64url encoded
	b := make([]byte, 32)
	rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)

	// Challenge: SHA256(verifier), base64url encoded
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return
}

func generateRandomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	if err != nil {
		slog.Warn("failed to open browser", "error", err)
	}
}

func init() {
	remote.Register("geminioauth", func(name string, options map[string]string, resolve remote.Resolver) (remote.Provider, error) {
		prov := &GeminiOAuthProvider{
			name:     name,
			options:  options, // keep a copy to read initial token
			resolver: resolve,
		}

		// Initialize base provider with our factory
		base := &gemini.GeminiProvider{
			APIKey:        "",
			ClientFactory: prov.clientFactory,
		}
		prov.base = base

		return prov, nil
	})
}
