package geminioauth

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	json "github.com/goccy/go-json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/providers/gemini"
	"github.com/lucasew/mclone/pkg/remote"
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
	options  map[string]any
	resolver remote.Resolver
	token    *TokenData
	loginMu  sync.Mutex
}

func (p *GeminiOAuthProvider) Name() string { return "geminioauth" }

func (p *GeminiOAuthProvider) List(ctx context.Context) ([]remote.Model, error) {
	// Trigger authentication flow if needed
	if err := p.ensureToken(ctx); err != nil {
		return nil, err
	}

	// We cannot use the public Gemini API ListModels endpoint due to insufficient scopes
	// (Cloud Code token only has cloud-platform scope, not generativelanguage scope).
	// We return a static list of models known to be supported by Cloud Code.
	return []remote.Model{
		{Name: "Gemini 2.0 Flash", Slug: "gemini-2.0-flash"},
		{Name: "Gemini 1.5 Pro", Slug: "gemini-1.5-pro"},
		{Name: "Gemini 1.5 Flash", Slug: "gemini-1.5-flash"},
		{Name: "Gemini 1.0 Pro", Slug: "gemini-1.0-pro"},
	}, nil
}

func (p *GeminiOAuthProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	if err := p.ensureToken(ctx); err != nil {
		out := make(chan message.ChatResponse)
		go func() {
			out <- message.ChatResponse{Error: err}
			close(out)
		}()
		return out, nil
	}

	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)

		// Patch messages to include dummy thought signature for Gemini 3 models
		// to avoid ToGeminiContents dropping them (mimicking plugin behavior).
		patchedMessages := make([]message.Message, len(messages))
		for i, m := range messages {
			var newParts []message.Part
			for _, p := range m.Parts {
				if tc, ok := p.(message.ToolCallPart); ok {
					if len(tc.ThoughtSignature) == 0 {
						tc.ThoughtSignature = []byte("skip_thought_signature_validator")
					}
					// Also ensure new parts are created with this signature
					newParts = append(newParts, tc)
				} else {
					newParts = append(newParts, p)
				}
			}
			// Copy structure but replace parts
			newMsg := m
			newMsg.Parts = newParts
			patchedMessages[i] = newMsg
		}

		contents, sys := gemini.ToGeminiContents(patchedMessages)

		genConfig := map[string]interface{}{}
		if options.Temperature != nil {
			genConfig["temperature"] = *options.Temperature
		}
		if options.MaxTokens != nil {
			genConfig["maxOutputTokens"] = *options.MaxTokens
		}
		if options.TopP != nil {
			genConfig["topP"] = *options.TopP
		}
		if len(options.Stop) > 0 {
			genConfig["stopSequences"] = options.Stop
		}
		if options.JSONMode {
			genConfig["responseMimeType"] = "application/json"
		}

		reqPayload := map[string]interface{}{
			"contents":         contents,
			"generationConfig": genConfig,
		}
		if sys != nil {
			reqPayload["systemInstruction"] = sys
		}
		if len(options.Tools) > 0 {
			reqPayload["tools"] = gemini.ToGeminiTools(options.Tools)
		}

		// Cloud Code API expects a wrapped body: { model: "...", request: { ... } }
		wrappedBody := map[string]interface{}{
			"model":   modelName,
			"request": reqPayload,
		}
		// Inject project ID if available
		if p.token.ProjectID != "" {
			wrappedBody["project"] = p.token.ProjectID
			slog.Info("gemini_oauth_chat_project", "project_id", p.token.ProjectID)
		} else {
			slog.Warn("gemini_oauth_chat_no_project", "msg", "Project ID missing, request might fail")
		}

		bodyBytes, err := json.Marshal(wrappedBody)
		if err != nil {
			out <- message.ChatResponse{Error: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}

		// URL for streaming: v1internal:streamGenerateContent?alt=sse
		url := "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse"

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			out <- message.ChatResponse{Error: err}
			return
		}

		req.Header.Set("Authorization", "Bearer "+p.token.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
		req.Header.Set("X-Goog-Api-Client", "gl-node/22.17.0")
		req.Header.Set("Client-Metadata", "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- message.ChatResponse{Error: err}
			return
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode == 429 || resp.StatusCode == 503 {
				retryAfter := parseRetryAfter(resp, body)
				slog.Warn("gemini_rate_limit", "status", resp.StatusCode, "retry_after", retryAfter)
				out <- message.ChatResponse{Error: &message.ErrRateLimit{RetryAfter: retryAfter}}
				return
			}

			out <- message.ChatResponse{Error: fmt.Errorf("api error %d: %s", resp.StatusCode, string(body))}
			return
		}
		defer resp.Body.Close()

		// Parse SSE stream
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					out <- message.ChatResponse{Error: err}
				}
				break
			}

			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			dataStr := strings.TrimPrefix(line, "data: ")
			if dataStr == "[DONE]" {
				break
			}

			// SSE Response Struct (supporting both wrapped and unwrapped, and function calls)
			type CandidatePart struct {
				Text         string `json:"text,omitempty"`
				FunctionCall *struct {
					Name string                 `json:"name"`
					Args map[string]interface{} `json:"args"`
				} `json:"functionCall,omitempty"`
			}

			type Candidate struct {
				Content struct {
					Parts []CandidatePart `json:"parts"`
				} `json:"content"`
			}

			var sseResp struct {
				Response struct {
					Candidates []Candidate `json:"candidates"`
				} `json:"response"`
			}

			var standardResp struct {
				Candidates []Candidate `json:"candidates"`
			}

			var candidates []Candidate

			// Cloud Code SSE payloads might be wrapped or just standard.
			if err := json.Unmarshal([]byte(dataStr), &sseResp); err == nil && len(sseResp.Response.Candidates) > 0 {
				candidates = sseResp.Response.Candidates
			} else if err := json.Unmarshal([]byte(dataStr), &standardResp); err == nil && len(standardResp.Candidates) > 0 {
				candidates = standardResp.Candidates
			}

			for _, cand := range candidates {
				for _, part := range cand.Content.Parts {
					if part.Text != "" {
						out <- message.ChatResponse{Content: part.Text}
					}
					if part.FunctionCall != nil {
						// Convert args to JSON bytes for ToolCall
						argsBytes, _ := json.Marshal(part.FunctionCall.Args)
						out <- message.ChatResponse{
							ToolCalls: []message.ToolCall{{
								ID:        "call_" + generateRandomString(8), // Gemini doesn't always send ID in stream?
								Name:      part.FunctionCall.Name,
								Arguments: json.RawMessage(argsBytes),
							}},
						}
					}
				}
			}
		}

		out <- message.ChatResponse{Done: true}
	}()
	return out, nil
}

// Token management

type TokenData struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"` // seconds
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`
	ProjectID    string    `json:"project_id,omitempty"` // Derived from Cloud Code onboarding
}

func (p *GeminiOAuthProvider) ensureToken(ctx context.Context) error {
	// 1. Fast path: check if valid token exists in memory
	token, err := p.loadToken()
	if err == nil && token != nil && time.Now().Add(time.Minute).Before(token.ExpiresAt) {
		p.base.APIKey = token.AccessToken
		return nil
	}

	// 2. Slow path: Refresh or Login (protected by Mutex)
	p.loginMu.Lock()
	defer p.loginMu.Unlock()

	// Re-check after acquiring lock
	token, err = p.loadToken()
	if err == nil && token != nil && time.Now().Add(time.Minute).Before(token.ExpiresAt) {
		p.base.APIKey = token.AccessToken
		return nil
	}

	// Try refresh
	if token != nil && token.RefreshToken != "" {
		slog.Info("gemini_oauth_refreshing_token")
		newToken, err := p.refreshToken(token.RefreshToken)
		if err == nil {
			if err := p.saveToken(newToken); err != nil {
				monitor.ReportError(ctx, err, "action", "gemini_oauth_save_token_failed")
			}
			p.base.APIKey = newToken.AccessToken
			return nil
		}
		monitor.ReportError(ctx, err, "action", "gemini_oauth_refresh_failed")
	}

	// Login
	slog.Info("gemini_oauth_login_required")
	newToken, err := p.performLoginFlow(ctx)
	if err != nil {
		return err
	}

	// Ensure we have a project ID (onboard to free tier if needed)
	if newToken.ProjectID == "" {
		pid, err := p.onboardManagedProject(ctx, newToken.AccessToken)
		if err != nil {
			slog.Warn("gemini_onboard_failed", "error", err)
			// Continue anyway? API might fail later.
		} else {
			newToken.ProjectID = pid
		}
	}

	if err := p.saveToken(newToken); err != nil {
		monitor.ReportError(ctx, err, "action", "gemini_oauth_save_token_failed")
	}
	p.base.APIKey = newToken.AccessToken
	return nil
}

func (p *GeminiOAuthProvider) onboardManagedProject(ctx context.Context, accessToken string) (string, error) {
	// Mimic plugin's onboardManagedProject with tierId="free-tier"
	url := "https://cloudcode-pa.googleapis.com/v1internal:onboardUser"

	metadata := map[string]string{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	}

	bodyData := map[string]interface{}{
		"tierId":   "free-tier",
		"metadata": metadata,
	}

	bodyBytes, _ := json.Marshal(bodyData)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.17.0")
	req.Header.Set("Client-Metadata", "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("onboard error %d: %s", resp.StatusCode, string(respBody))
	}

	var payload struct {
		Name     string `json:"name"` // Operation name
		Done     bool   `json:"done"`
		Response struct {
			CloudAICompanionProject struct {
				ID string `json:"id"`
			} `json:"cloudaicompanionProject"`
		} `json:"response"`
	}

	if err := json.Unmarshal(respBody, &payload); err != nil {
		return "", err
	}

	if payload.Done {
		return payload.Response.CloudAICompanionProject.ID, nil
	}

	// If not done, poll the operation
	if payload.Name == "" {
		return "", fmt.Errorf("onboard incomplete and no operation name returned")
	}

	opName := payload.Name
	// Poll for up to 60 seconds
	for i := 0; i < 12; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
		}

		opUrl := "https://cloudcode-pa.googleapis.com/v1internal/" + opName
		reqOp, _ := http.NewRequestWithContext(ctx, "GET", opUrl, nil)
		reqOp.Header = req.Header

		respOp, err := http.DefaultClient.Do(reqOp)
		if err != nil {
			continue
		}

		bodyOp, _ := io.ReadAll(respOp.Body)
		respOp.Body.Close()

		if respOp.StatusCode != 200 {
			continue
		}

		if err := json.Unmarshal(bodyOp, &payload); err != nil {
			continue
		}

		if payload.Done {
			return payload.Response.CloudAICompanionProject.ID, nil
		}
	}

	return "", fmt.Errorf("onboard timed out")
}

func (p *GeminiOAuthProvider) loadToken() (*TokenData, error) {
	if p.token != nil {
		return p.token, nil
	}

	// Load from options
	tokenJSON, ok := p.options["token"].(string)
	if !ok || tokenJSON == "" {
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
		updates := map[string]any{
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

	mux := http.NewServeMux()
	mux.HandleFunc(redirectPath, func(w http.ResponseWriter, r *http.Request) {
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
		if _, err := w.Write([]byte("Login successful! You can close this window.")); err != nil {
			monitor.ReportError(context.Background(), err, "action", "gemini_oauth_response_write_failed")
		}
		codeCh <- code
	})

	srv := &http.Server{Addr: ":" + redirectPort, Handler: mux}

	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	defer func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			monitor.ReportError(context.Background(), err, "action", "gemini_oauth_server_shutdown_failed")
		}
	}()

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
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)

	// Challenge: SHA256(verifier), base64url encoded
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return
}

func generateRandomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// parseRetryAfter extracts a retry delay from the response.
// Checks Google RPC RetryInfo in the JSON body, then Retry-After header.
// Returns 0 if no hint was found (caller should use backoff).
func parseRetryAfter(resp *http.Response, body []byte) time.Duration {
	var delay time.Duration

	// Try Google RPC RetryInfo in error details
	var errResp struct {
		Error struct {
			Details []map[string]interface{}
		}
	}
	if json.Unmarshal(body, &errResp) == nil {
		for _, det := range errResp.Error.Details {
			if det["@type"] == "type.googleapis.com/google.rpc.RetryInfo" {
				if retryDelayStr, ok := det["retryDelay"].(string); ok {
					if d, err := time.ParseDuration(retryDelayStr); err == nil {
						delay = d + 100*time.Millisecond
					}
				}
			}
		}
	}

	// Retry-After header overrides
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if d, err := time.ParseDuration(ra + "s"); err == nil {
			delay = d
		}
	}

	return delay
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
	remote.Register("geminioauth", func(name string, options map[string]any, resolve remote.Resolver) (remote.Provider, error) {
		prov := &GeminiOAuthProvider{
			name:     name,
			options:  options, // keep a copy to read initial token
			resolver: resolve,
		}

		// Initialize base provider with our factory
		base := &gemini.GeminiProvider{
			APIKey: "",
		}
		prov.base = base

		return prov, nil
	})
}
