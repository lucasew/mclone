package antigravity

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/providers/gemini"
	"github.com/lucasew/mclone/pkg/remote"
)

const (
	redirectPort = "36742"
	redirectPath = "/oauth-callback"
	redirectURI  = "http://localhost:" + redirectPort + redirectPath
	authURL      = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenURL     = "https://oauth2.googleapis.com/token"
	userAgent    = "antigravity/1.20.6"
)

func invertBits(s string) string {
	b := []byte(s)
	for i := range b {
		b[i] = ^b[i]
	}
	return string(b)
}

var (
	clientID     = invertBits("\xce\xcf\xc8\xce\xcf\xcf\xc9\xcf\xc9\xcf\xca\xc6\xce\xd2\x8b\x92\x97\x8c\x8c\x96\x91\xcd\x97\xcd\xce\x93\x9c\x8d\x9a\xcd\xcc\xca\x89\x8b\x90\x93\x90\x95\x97\xcb\x98\xcb\xcf\xcc\x9a\x8f\xd1\x9e\x8f\x8f\x8c\xd1\x98\x90\x90\x98\x93\x9a\x8a\x8c\x9a\x8d\x9c\x90\x91\x8b\x9a\x91\x8b\xd1\x9c\x90\x92")
	clientSecret = invertBits("\xb8\xb0\xbc\xac\xaf\xa7\xd2\xb4\xca\xc7\xb9\xa8\xad\xcb\xc7\xc9\xb3\x9b\xb3\xb5\xce\x92\xb3\xbd\xc7\x8c\xa7\xbc\xcb\x85\xc9\x8e\xbb\xbe\x99")
)

var scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

var codeAssistEndpoints = []string{
	"https://autopush-cloudcode-pa.sandbox.googleapis.com",
	"https://cloudcode-pa.googleapis.com",
}

var modelAliases = map[string]string{
	"gemini-2.5-pro":                    "gemini-2.5-pro-exp-03-25",
	"gemini-2.5-flash":                  "gemini-2.5-flash",
	"gemini-2.5-flash-lite":             "gemini-2.5-flash-lite-001",
	"gemini-claude-sonnet-4-5":          "claude-sonnet-4-5",
	"gemini-claude-sonnet-4-5-thinking": "claude-sonnet-4-5-thinking",
	"gemini-claude-opus-4-5":            "claude-opus-4-5",
	"gemini-claude-opus-4-5-thinking":   "claude-opus-4-5-thinking",
}

type Provider struct {
	base          *gemini.GeminiProvider
	name          string
	options       map[string]any
	resolver      remote.Resolver
	token         *TokenData
	loginMu       sync.Mutex
	retryCfg      RetryConfig
	retryMu       sync.Mutex
	retrySeq      int
	endpointMu    sync.Mutex
	endpointIndex int
}

type RetryConfig struct {
	RetryAfterThreshold time.Duration
	BackoffInitial      time.Duration
	BackoffMax          time.Duration
}

const (
	defaultRetryAfterThreshold = 15 * time.Second
	defaultBackoffInitial      = 1 * time.Second
	defaultBackoffMax          = 8 * time.Second
)

func (p *Provider) Name() string { return "antigravity" }

func (p *Provider) List(ctx context.Context) ([]remote.Model, error) {
	if err := p.ensureToken(ctx); err != nil {
		return nil, err
	}
	return []remote.Model{
		{Name: "Gemini 3 Pro (Antigravity)", Slug: "antigravity-gemini-3-pro"},
		{Name: "Gemini 3.1 Pro (Antigravity)", Slug: "antigravity-gemini-3.1-pro"},
		{Name: "Gemini 3 Flash (Antigravity)", Slug: "antigravity-gemini-3-flash"},
		{Name: "Claude Sonnet 4.6 (Antigravity)", Slug: "antigravity-claude-sonnet-4-6"},
		{Name: "Claude Opus 4.6 Thinking (Antigravity)", Slug: "antigravity-claude-opus-4-6-thinking"},
		{Name: "Gemini 2.5 Flash (Gemini CLI)", Slug: "gemini-2.5-flash"},
		{Name: "Gemini 2.5 Pro (Gemini CLI)", Slug: "gemini-2.5-pro"},
		{Name: "Gemini 3 Flash Preview (Gemini CLI)", Slug: "gemini-3-flash-preview"},
		{Name: "Gemini 3 Pro Preview (Gemini CLI)", Slug: "gemini-3-pro-preview"},
		{Name: "Gemini 3.1 Pro Preview (Gemini CLI)", Slug: "gemini-3.1-pro-preview"},
		{Name: "Gemini 3.1 Pro Preview Custom Tools (Gemini CLI)", Slug: "gemini-3.1-pro-preview-customtools"},
	}, nil
}

func (p *Provider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	if err := p.ensureToken(ctx); err != nil {
		out := make(chan message.Event)
		go func() {
			out <- message.ResponseError{Err: err}
			close(out)
		}()
		return out, nil
	}

	out := make(chan message.Event)
	go func() {
		defer close(out)

		patchedMessages := make([]message.Turn, len(req.Turns))
		for i, m := range req.Turns {
			var newParts []message.Part
			for _, p := range m.Parts {
				if tc, ok := p.(message.ToolCallPart); ok {
					if len(tc.ThoughtSignature) == 0 {
						tc.ThoughtSignature = []byte("skip_thought_signature_validator")
					}
					newParts = append(newParts, tc)
				} else {
					newParts = append(newParts, p)
				}
			}
			newMsg := m
			newMsg.Parts = newParts
			patchedMessages[i] = newMsg
		}

		contents, sys := gemini.ToGeminiContents(patchedMessages)

		genConfig := map[string]interface{}{}
		if req.Options.Temperature != nil {
			genConfig["temperature"] = *req.Options.Temperature
		}
		if req.Options.MaxTokens != nil {
			genConfig["maxOutputTokens"] = *req.Options.MaxTokens
		}
		if req.Options.TopP != nil {
			genConfig["topP"] = *req.Options.TopP
		}
		if len(req.Options.Stop) > 0 {
			genConfig["stopSequences"] = req.Options.Stop
		}
		if req.Options.JSONMode {
			genConfig["responseMimeType"] = "application/json"
		}

		reqPayload := map[string]interface{}{
			"contents":         contents,
			"generationConfig": genConfig,
		}
		if sys != nil {
			reqPayload["systemInstruction"] = sys
		}
		if len(req.Options.Tools) > 0 {
			if isClaudeModel(req.Model) {
				reqPayload["tools"] = toClaudeAntigravityTools(req.Options.Tools)
				reqPayload["toolConfig"] = map[string]any{
					"functionCallingConfig": map[string]any{
						"mode": "VALIDATED",
					},
				}
			} else {
				reqPayload["tools"] = gemini.ToGeminiTools(req.Options.Tools)
			}
		}

		sessionID := "session-" + generateRandomString(16)
		reqPayload["sessionId"] = sessionID
		wrappedBody := map[string]interface{}{
			"model":       normalizeGeminiModel(req.Model),
			"request":     reqPayload,
			"requestType": "agent",
			"userAgent":   "antigravity",
			"requestId":   "agent-" + generateRandomString(16),
		}
		if p.token.ProjectID != "" {
			wrappedBody["project"] = p.token.ProjectID
			slog.Info("antigravity_chat_project", "project_id", p.token.ProjectID)
		} else {
			slog.Warn("antigravity_chat_no_project", "msg", "Project ID missing, request might fail")
		}

		bodyBytes, err := json.Marshal(wrappedBody)
		if err != nil {
			out <- message.ResponseError{Err: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", "/v1internal:streamGenerateContent?alt=sse", bytes.NewReader(bodyBytes))
		if err != nil {
			out <- message.ResponseError{Err: err}
			return
		}

		httpReq.Header.Set("Authorization", "Bearer "+p.token.AccessToken)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("User-Agent", userAgent)

		resp, err := p.doChatRequest(ctx, httpReq, bodyBytes)
		if err != nil {
			out <- message.ResponseError{Err: err}
			return
		}
		defer resp.Body.Close()

		reader := bufio.NewReader(resp.Body)
		toolCallsBuffer := make(map[int]*message.ToolCall)
		var toolCallOrder []int
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					out <- message.ResponseError{Err: err}
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
			if err := json.Unmarshal([]byte(dataStr), &sseResp); err == nil && len(sseResp.Response.Candidates) > 0 {
				candidates = sseResp.Response.Candidates
			} else if err := json.Unmarshal([]byte(dataStr), &standardResp); err == nil && len(standardResp.Candidates) > 0 {
				candidates = standardResp.Candidates
			}

			for _, cand := range candidates {
				for i, part := range cand.Content.Parts {
					if part.Text != "" {
						out <- message.TextDelta{Text: part.Text}
					}
					if part.FunctionCall != nil {
						tc, ok := toolCallsBuffer[i]
						if !ok {
							tc = &message.ToolCall{ID: fmt.Sprintf("toolu_oauth_%d_%d", time.Now().UnixNano()%1000000, i), Name: part.FunctionCall.Name}
							toolCallsBuffer[i] = tc
							toolCallOrder = append(toolCallOrder, i)
						}
						if part.FunctionCall.Name != "" {
							tc.Name = part.FunctionCall.Name
						}
						if len(part.FunctionCall.Args) > 0 {
							currentArgs := make(map[string]interface{})
							if len(tc.Arguments) > 0 {
								if err := json.Unmarshal(tc.Arguments, &currentArgs); err != nil {
									monitor.ReportError(ctx, err, "action", "antigravity_arg_merge_error")
								}
							}
							for k, v := range part.FunctionCall.Args {
								currentArgs[k] = v
							}
							argsBytes, err := json.Marshal(currentArgs)
							if err != nil {
								monitor.ReportError(ctx, err, "action", "antigravity_arg_marshal_error")
							} else {
								tc.Arguments = json.RawMessage(argsBytes)
							}
						}
					}
				}
			}
		}

		if len(toolCallOrder) > 0 {
			for _, idx := range toolCallOrder {
				out <- message.ToolCallFinished{Call: *toolCallsBuffer[idx]}
			}
			out <- message.ResponseCompleted{Reason: message.StopReasonToolCall}
			return
		}
		out <- message.ResponseCompleted{Reason: message.StopReasonEndTurn}
	}()
	return out, nil
}

func (p *Provider) doChatRequest(ctx context.Context, request *http.Request, body []byte) (*http.Response, error) {
	threshold := p.retryCfg.RetryAfterThreshold
	if threshold <= 0 {
		threshold = defaultRetryAfterThreshold
	}
	backoffInitial := p.retryCfg.BackoffInitial
	if backoffInitial <= 0 {
		backoffInitial = defaultBackoffInitial
	}
	backoffMax := p.retryCfg.BackoffMax
	if backoffMax <= 0 {
		backoffMax = defaultBackoffMax
	}

	for attempt := 0; ; attempt++ {
		var lastErr error
		start := p.getEndpointIndex()
		for i := 0; i < len(codeAssistEndpoints); i++ {
			endpointIndex := (start + i) % len(codeAssistEndpoints)
			endpoint := codeAssistEndpoints[endpointIndex]
			req := request.Clone(ctx)
			req.URL.Scheme = "https"
			req.URL.Host = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
			req.ContentLength = int64(len(body))

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				lastErr = err
				slog.Warn("antigravity_endpoint_error", "endpoint", endpoint, "error", err)
				continue
			}
			if resp.StatusCode == http.StatusOK {
				p.resetRateLimitBackoff()
				p.setEndpointIndex(endpointIndex)
				return resp, nil
			}
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
				respBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				retryAfter := parseRetryAfter(resp, respBody)
				retrySeq := 0
				if retryAfter <= 0 {
					retryAfter, retrySeq = p.nextRateLimitBackoff(backoffInitial, backoffMax)
				} else {
					p.resetRateLimitBackoff()
				}
				slog.Warn("antigravity_rate_limit", "endpoint", endpoint, "status", resp.StatusCode, "retry_after", retryAfter, "retry_seq", retrySeq, "attempt", attempt+1, "threshold", threshold)
				if retryAfter > threshold {
					return nil, &message.ErrRateLimit{RetryAfter: retryAfter}
				}
				timer := time.NewTimer(retryAfter)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
				}
				lastErr = &message.ErrRateLimit{RetryAfter: retryAfter}
				goto nextAttempt
			}
			if resp.StatusCode >= 500 {
				respBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				lastErr = fmt.Errorf("endpoint %s server error %d: %s", endpoint, resp.StatusCode, string(respBody))
				slog.Warn("antigravity_endpoint_server_error", "endpoint", endpoint, "status", resp.StatusCode)
				continue
			}
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusForbidden && shouldFailoverEndpoint(endpoint, respBody) {
				lastErr = fmt.Errorf("endpoint %s forbidden: %s", endpoint, string(respBody))
				slog.Warn("antigravity_endpoint_forbidden_failover", "endpoint", endpoint, "status", resp.StatusCode)
				continue
			}
			p.setEndpointIndex(endpointIndex)
			return nil, fmt.Errorf("api error %d: %s", resp.StatusCode, string(respBody))
		}
		if lastErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			slog.Warn("antigravity_all_endpoints_failed", "attempt", attempt+1, "error", lastErr)
		}
	nextAttempt:
	}
}

func backoffDuration(sequence int, initial, max time.Duration) time.Duration {
	backoff := initial << sequence
	if backoff > max {
		return max
	}
	return backoff
}

func normalizeGeminiModel(model string) string {
	model = strings.TrimPrefix(model, "models/")
	return resolveAntigravityModel(model)
}

func resolveAntigravityModel(model string) string {
	model = strings.TrimPrefix(model, "antigravity-")
	model = strings.TrimSuffix(model, "-preview-customtools")
	model = strings.TrimSuffix(model, "-preview")

	if alias, ok := modelAliases[model]; ok {
		model = alias
	}

	switch {
	case isGemini3ProModel(model):
		if !hasTierSuffix(model) && !isImageGenerationModel(model) {
			return model + "-low"
		}
	case isGemini3FlashModel(model):
		if hasTierSuffix(model) {
			return stripTierSuffix(model)
		}
	}

	return model
}

func hasTierSuffix(model string) bool {
	return strings.HasSuffix(model, "-low") ||
		strings.HasSuffix(model, "-medium") ||
		strings.HasSuffix(model, "-high") ||
		strings.HasSuffix(model, "-minimal")
}

func stripTierSuffix(model string) string {
	for _, suffix := range []string{"-minimal", "-low", "-medium", "-high"} {
		if strings.HasSuffix(model, suffix) {
			return strings.TrimSuffix(model, suffix)
		}
	}
	return model
}

func isGemini3ProModel(model string) bool {
	model = strings.ToLower(model)
	return strings.HasPrefix(model, "gemini-3-pro") || strings.HasPrefix(model, "gemini-3.1-pro")
}

func isGemini3FlashModel(model string) bool {
	model = strings.ToLower(model)
	return strings.HasPrefix(model, "gemini-3-flash") || strings.HasPrefix(model, "gemini-3.1-flash")
}

func isImageGenerationModel(model string) bool {
	model = strings.ToLower(model)
	return strings.Contains(model, "image") || strings.Contains(model, "imagen")
}

func isClaudeModel(model string) bool {
	model = strings.TrimPrefix(strings.ToLower(model), "antigravity-")
	return strings.Contains(model, "claude")
}

func toClaudeAntigravityTools(tools []message.ToolDefinition) []map[string]any {
	functionDeclarations := make([]map[string]any, 0, len(tools))
	for i, tool := range tools {
		name := tool.Name
		if name == "" {
			name = fmt.Sprintf("tool_%d", i)
		}
		name = sanitizeToolName(name)

		schema := tool.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		} else {
			schema = sanitizeToolSchema(schema)
		}

		functionDeclarations = append(functionDeclarations, map[string]any{
			"name":        name,
			"description": tool.Description,
			"parameters":  schema,
		})
	}
	if len(functionDeclarations) == 0 {
		return nil
	}
	return []map[string]any{{
		"functionDeclarations": functionDeclarations,
	}}
}

func sanitizeToolSchema(raw json.RawMessage) json.RawMessage {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	cleaned := sanitizeSchemaValue(value)
	out, err := json.Marshal(cleaned)
	if err != nil {
		return raw
	}
	return out
}

func sanitizeSchemaValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			switch k {
			case "$schema", "propertyNames", "const", "exclusiveMinimum", "exclusiveMaximum":
				continue
			}
			out[k] = sanitizeSchemaValue(vv)
		}
		return out
	case []any:
		for i := range x {
			x[i] = sanitizeSchemaValue(x[i])
		}
		return x
	default:
		return v
	}
}

func sanitizeToolName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		return "tool"
	}
	return b.String()
}

func shouldFailoverEndpoint(endpoint string, body []byte) bool {
	if !strings.Contains(endpoint, "sandbox.googleapis.com") {
		return false
	}
	type errorEnvelope struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				Type     string            `json:"@type"`
				Reason   string            `json:"reason"`
				Domain   string            `json:"domain"`
				Metadata map[string]string `json:"metadata"`
			} `json:"details"`
			Errors []struct {
				Reason string `json:"reason"`
				Domain string `json:"domain"`
			} `json:"errors"`
		} `json:"error"`
	}
	check := func(errResp errorEnvelope) bool {
		if errResp.Error.Status == "PERMISSION_DENIED" {
			for _, detail := range errResp.Error.Details {
				if detail.Reason == "SERVICE_DISABLED" {
					return true
				}
			}
		}
		for _, item := range errResp.Error.Errors {
			if item.Reason == "accessNotConfigured" && item.Domain == "usageLimits" {
				return true
			}
		}
		return false
	}
	var errResp errorEnvelope
	if err := json.Unmarshal(body, &errResp); err == nil {
		return check(errResp)
	}
	var errList []errorEnvelope
	if err := json.Unmarshal(body, &errList); err == nil {
		for _, item := range errList {
			if check(item) {
				return true
			}
		}
	}
	return false
}

func (p *Provider) nextRateLimitBackoff(initial, max time.Duration) (time.Duration, int) {
	p.retryMu.Lock()
	defer p.retryMu.Unlock()
	seq := p.retrySeq
	backoff := backoffDuration(seq, initial, max)
	p.retrySeq++
	return backoff, seq
}

func (p *Provider) resetRateLimitBackoff() {
	p.retryMu.Lock()
	defer p.retryMu.Unlock()
	p.retrySeq = 0
}

func (p *Provider) getEndpointIndex() int {
	p.endpointMu.Lock()
	defer p.endpointMu.Unlock()
	return p.endpointIndex
}

func (p *Provider) setEndpointIndex(idx int) {
	p.endpointMu.Lock()
	defer p.endpointMu.Unlock()
	p.endpointIndex = idx
}

type TokenData struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`
	ProjectID    string    `json:"project_id,omitempty"`
}

func (p *Provider) ensureToken(ctx context.Context) error {
	token, err := p.loadToken()
	if err == nil && token != nil && time.Now().Add(time.Minute).Before(token.ExpiresAt) {
		if token.ProjectID == "" {
			slog.Info("antigravity_project_missing_on_token")
			pid, onboardErr := p.onboardManagedProject(ctx, token.AccessToken)
			if onboardErr != nil {
				monitor.ReportError(ctx, onboardErr, "action", "antigravity_onboard_missing_project_failed")
			} else {
				token.ProjectID = pid
				if saveErr := p.saveToken(token); saveErr != nil {
					monitor.ReportError(ctx, saveErr, "action", "antigravity_save_token_failed")
				}
			}
		}
		p.base.APIKey = token.AccessToken
		return nil
	}
	p.loginMu.Lock()
	defer p.loginMu.Unlock()
	token, err = p.loadToken()
	if err == nil && token != nil && time.Now().Add(time.Minute).Before(token.ExpiresAt) {
		if token.ProjectID == "" {
			slog.Info("antigravity_project_missing_on_token")
			pid, onboardErr := p.onboardManagedProject(ctx, token.AccessToken)
			if onboardErr != nil {
				monitor.ReportError(ctx, onboardErr, "action", "antigravity_onboard_missing_project_failed")
			} else {
				token.ProjectID = pid
				if saveErr := p.saveToken(token); saveErr != nil {
					monitor.ReportError(ctx, saveErr, "action", "antigravity_save_token_failed")
				}
			}
		}
		p.base.APIKey = token.AccessToken
		return nil
	}
	if token != nil && token.RefreshToken != "" {
		slog.Info("antigravity_refreshing_token")
		newToken, err := p.refreshToken(token.RefreshToken)
		if err == nil {
			if newToken.ProjectID == "" {
				pid, onboardErr := p.onboardManagedProject(ctx, newToken.AccessToken)
				if onboardErr != nil {
					monitor.ReportError(ctx, onboardErr, "action", "antigravity_onboard_missing_project_failed")
				} else {
					newToken.ProjectID = pid
				}
			}
			if err := p.saveToken(newToken); err != nil {
				monitor.ReportError(ctx, err, "action", "antigravity_save_token_failed")
			}
			p.base.APIKey = newToken.AccessToken
			return nil
		}
		monitor.ReportError(ctx, err, "action", "antigravity_refresh_failed")
	}
	slog.Info("antigravity_login_required")
	newToken, err := p.performLoginFlow(ctx)
	if err != nil {
		return err
	}
	if newToken.ProjectID == "" {
		pid, err := p.onboardManagedProject(ctx, newToken.AccessToken)
		if err != nil {
			slog.Warn("antigravity_onboard_failed", "error", err)
		} else {
			newToken.ProjectID = pid
		}
	}
	if err := p.saveToken(newToken); err != nil {
		monitor.ReportError(ctx, err, "action", "antigravity_save_token_failed")
	}
	p.base.APIKey = newToken.AccessToken
	return nil
}

func (p *Provider) onboardManagedProject(ctx context.Context, accessToken string) (string, error) {
	url := "https://cloudcode-pa.googleapis.com/v1internal:onboardUser"
	metadata := map[string]string{"ideType": "IDE_UNSPECIFIED", "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI"}
	bodyData := map[string]interface{}{"tierId": "free-tier", "metadata": metadata}
	bodyBytes, _ := json.Marshal(bodyData)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
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
		Name     string `json:"name"`
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
	if payload.Name == "" {
		return "", fmt.Errorf("onboard incomplete and no operation name returned")
	}
	opName := payload.Name
	for i := 0; i < 12; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
		opURL := "https://cloudcode-pa.googleapis.com/v1internal/" + opName
		reqOp, _ := http.NewRequestWithContext(ctx, "GET", opURL, nil)
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

func (p *Provider) loadToken() (*TokenData, error) {
	if p.token != nil {
		return p.token, nil
	}
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

func (p *Provider) saveToken(token *TokenData) error {
	p.token = token
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}
	if p.resolver.UpdateOptions != nil {
		return p.resolver.UpdateOptions(p.name, map[string]any{"token": string(data)})
	}
	return nil
}

func (p *Provider) refreshToken(refreshToken string) (*TokenData, error) {
	data := url.Values{}
	data.Set("client_id", clientID)
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
	newToken.RefreshToken = refreshToken
	newToken.ExpiresAt = time.Now().Add(time.Duration(newToken.ExpiresIn) * time.Second)
	if newToken.RefreshToken == "" {
		newToken.RefreshToken = refreshToken
	}
	return &newToken, nil
}

func (p *Provider) performLoginFlow(ctx context.Context) (*TokenData, error) {
	verifier, challenge := generatePKCE()
	state := generateRandomString(16)
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
			monitor.ReportError(context.Background(), err, "action", "antigravity_response_write_failed")
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
			monitor.ReportError(context.Background(), err, "action", "antigravity_server_shutdown_failed")
		}
	}()
	u, _ := url.Parse(authURL)
	q := u.Query()
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	u.RawQuery = q.Encode()
	fmt.Printf("Opening browser to authenticate: %s\n", u.String())
	openBrowser(u.String())
	select {
	case code := <-codeCh:
		return p.exchangeCode(code, verifier)
	case err := <-errCh:
		return nil, err
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("timeout waiting for login")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Provider) exchangeCode(code, verifier string) (*TokenData, error) {
	data := url.Values{}
	data.Set("client_id", clientID)
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

func generatePKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
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

func parseRetryAfter(resp *http.Response, body []byte) time.Duration {
	var delay time.Duration
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
	remote.Register("antigravity", func(name string, options map[string]any, resolve remote.Resolver) (remote.Provider, error) {
		retryCfg := RetryConfig{
			RetryAfterThreshold: defaultRetryAfterThreshold,
			BackoffInitial:      defaultBackoffInitial,
			BackoffMax:          defaultBackoffMax,
		}
		if raw, ok := options["retry_after_threshold"]; ok {
			if v, ok := raw.(string); ok {
				if d, err := time.ParseDuration(v); err == nil {
					retryCfg.RetryAfterThreshold = d
				}
			}
		}
		if raw, ok := options["retry_backoff_initial"]; ok {
			if v, ok := raw.(string); ok {
				if d, err := time.ParseDuration(v); err == nil {
					retryCfg.BackoffInitial = d
				}
			}
		}
		if raw, ok := options["retry_backoff_max"]; ok {
			if v, ok := raw.(string); ok {
				if d, err := time.ParseDuration(v); err == nil {
					retryCfg.BackoffMax = d
				}
			}
		}
		prov := &Provider{name: name, options: options, resolver: resolve, retryCfg: retryCfg}
		prov.base = &gemini.GeminiProvider{APIKey: ""}
		return prov, nil
	})

	remote.Register("geminioauth", func(name string, options map[string]any, resolve remote.Resolver) (remote.Provider, error) {
		return nil, fmt.Errorf("provider type %q was renamed to %q; update your config", "geminioauth", "antigravity")
	})
}
