package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
	"github.com/lucasew/mclone/pkg/chatui"
	"github.com/tmc/langchaingo/llms"
	anthropicllm "github.com/tmc/langchaingo/llms/anthropic"
	openaillm "github.com/tmc/langchaingo/llms/openai"
)

var chatCmd = &cobra.Command{
	Use:   "chat [model]",
	Short: "Start a LangChainGo chat session against a running mclone server",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if err := runNativeChat(cmd, args[0]); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	},
}

type localTool interface {
	Name() string
	Schema() llms.Tool
	Call(ctx context.Context, args map[string]any) (string, error)
}

type shellTool struct{}

func (shellTool) Name() string { return "shell" }

func (shellTool) Schema() llms.Tool {
	return llms.Tool{
		Type: "function",
		Function: &llms.FunctionDefinition{
			Name:        "shell",
			Description: "Run a shell command in bash. Input must be the exact command line to execute.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Shell command to run with bash -lc",
					},
				},
				"required": []string{"command"},
			},
		},
	}
}

func (shellTool) Call(ctx context.Context, args map[string]any) (string, error) {
	command, _ := args["command"].(string)
	command = strings.TrimSpace(command)
	if command == "" {
		return "empty command", nil
	}

	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	output, err := cmd.CombinedOutput()
	text := truncateToolOutput(string(output))
	if err != nil {
		if text == "" {
			return err.Error(), nil
		}
		return fmt.Sprintf("%s\n\ncommand error: %v", text, err), nil
	}
	if text == "" {
		return "(no output)", nil
	}
	return text, nil
}

type readFileTool struct{}

func (readFileTool) Name() string { return "read_file" }

func (readFileTool) Schema() llms.Tool {
	return llms.Tool{
		Type: "function",
		Function: &llms.FunctionDefinition{
			Name:        "read_file",
			Description: "Read a UTF-8 text file from the current workspace. Input must be a relative or absolute file path.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path to read",
					},
				},
				"required": []string{"path"},
			},
		},
	}
}

func (readFileTool) Call(_ context.Context, args map[string]any) (string, error) {
	path, _ := args["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		return "empty path", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("read failed: %v", err), nil
	}
	return truncateToolOutput(string(data)), nil
}

type langchainRunner struct {
	model llms.Model
	tools map[string]localTool
	defs  []llms.Tool
}

type loggingRoundTripper struct {
	base http.RoundTripper
}

func runNativeChat(cmd *cobra.Command, target string) error {
	modelName := strings.TrimSpace(target)
	if modelName == "" {
		return errors.New("usage: mclone chat [model]")
	}
	backend, _ := cmd.Flags().GetString("backend")
	apiKey, _ := cmd.Flags().GetString("api-key")
	maxIterations, _ := cmd.Flags().GetInt("max-iterations")
	baseURL := buildLangchainBaseURL(cmd, backend)

	runner, err := newLangchainRunner(backend, baseURL, modelName, apiKey)
	if err != nil {
		return err
	}

	program := tea.NewProgram(chatui.New(
		cmd.Context(),
		modelName,
		backend,
		maxIterations,
		func(ctx context.Context, prompt string, maxIterations int) ([]chatui.Line, error) {
			return runner.run(ctx, prompt, maxIterations)
		},
	))
	_, err = program.Run()
	return err
}

func buildLangchainBaseURL(cmd *cobra.Command, backend string) string {
	baseURL, _ := cmd.Flags().GetString("base-url")
	if baseURL != "" {
		return normalizeBaseURL(baseURL, backend)
	}
	port, _ := cmd.Flags().GetInt("port")
	return normalizeBaseURL(fmt.Sprintf("http://127.0.0.1:%d", port), backend)
}

func normalizeBaseURL(rawBaseURL, backend string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return rawBaseURL
	}

	path := strings.TrimSuffix(parsed.Path, "/")
	switch backend {
	case "anthropic":
		if path == "" {
			parsed.Path = ""
		} else {
			parsed.Path = path
		}
	default:
		if path == "" {
			parsed.Path = "/v1"
		} else if path != "/v1" {
			parsed.Path = path + "/v1"
		}
	}
	return strings.TrimSuffix(parsed.String(), "/")
}

func newLangchainRunner(backend, baseURL, modelName, token string) (*langchainRunner, error) {
	var model llms.Model
	var err error
	httpClient := &http.Client{
		Transport: loggingRoundTripper{base: http.DefaultTransport},
	}

	switch backend {
	case "openai", "openai-legacy":
		model, err = openaillm.New(
			openaillm.WithBaseURL(baseURL),
			openaillm.WithModel(modelName),
			openaillm.WithToken(token),
			openaillm.WithHTTPClient(httpClient),
		)
	case "anthropic":
		model, err = anthropicllm.New(
			anthropicllm.WithBaseURL(baseURL),
			anthropicllm.WithModel(modelName),
			anthropicllm.WithToken(token),
			anthropicllm.WithHTTPClient(httpClient),
		)
	case "openai-responses":
		return nil, errors.New("langchaingo does not expose an OpenAI Responses API client in this version")
	default:
		return nil, fmt.Errorf("unknown langchain backend: %s", backend)
	}
	if err != nil {
		return nil, err
	}

	toolList := []localTool{
		shellTool{},
		readFileTool{},
	}
	defs := make([]llms.Tool, 0, len(toolList))
	tools := make(map[string]localTool, len(toolList))
	for _, tool := range toolList {
		defs = append(defs, tool.Schema())
		tools[tool.Name()] = tool
	}
	return &langchainRunner{
		model: model,
		tools: tools,
		defs:  defs,
	}, nil
}

func (runner *langchainRunner) run(ctx context.Context, prompt string, maxIterations int) ([]chatui.Line, error) {
	messages := []llms.MessageContent{
		{
			Role: llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{
				llms.TextPart("You are a debugging assistant. Use tools whenever the user asks you to inspect files or run commands."),
			},
		},
		{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(prompt)},
		},
	}
	lines := make([]chatui.Line, 0, maxIterations+1)

	for i := 0; i < maxIterations; i++ {
		logLangchainMessages(messages)
		resp, err := runner.model.GenerateContent(
			ctx,
			messages,
			llms.WithTools(runner.defs),
			llms.WithMaxTokens(1024),
		)
		if err != nil {
			return nil, err
		}
		if len(resp.Choices) == 0 {
			return nil, errors.New("no choices returned")
		}

		choice := resp.Choices[0]
		logLangchainChoice(choice)
		if len(choice.ToolCalls) == 0 {
			if choice.Content != "" {
				lines = append(lines, chatui.Line{Role: chatui.RoleAssistant, Text: choice.Content})
			}
			return lines, nil
		}

		assistantParts := make([]llms.ContentPart, 0, len(choice.ToolCalls)+1)
		if choice.Content != "" {
			lines = append(lines, chatui.Line{Role: chatui.RoleAssistant, Text: choice.Content})
			assistantParts = append(assistantParts, llms.TextPart(choice.Content))
		}
		for _, call := range choice.ToolCalls {
			assistantParts = append(assistantParts, call)
		}
		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: assistantParts,
		})

		for _, call := range choice.ToolCalls {
			logLangchainToolCall(call)
			result, err := runner.runToolCall(ctx, call)
			if err != nil {
				return nil, err
			}
			logLangchainToolResult(call, result)
			if call.FunctionCall != nil {
				lines = append(lines, chatui.Line{
					Role:   chatui.RoleTool,
					Text:   call.FunctionCall.Name,
					Detail: call.FunctionCall.Arguments,
					Status: toolCallStatus(result),
				})
			}
			messages = append(messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: call.ID,
						Name:       call.FunctionCall.Name,
						Content:    result,
					},
				},
			})
		}
	}

	return nil, fmt.Errorf("max iterations reached without final answer")
}

func (runner *langchainRunner) runToolCall(ctx context.Context, call llms.ToolCall) (string, error) {
	if call.FunctionCall == nil {
		return "missing function call payload", nil
	}
	tool, ok := runner.tools[call.FunctionCall.Name]
	if !ok {
		return fmt.Sprintf("unknown tool: %s", call.FunctionCall.Name), nil
	}

	args := make(map[string]any)
	if err := json.Unmarshal([]byte(call.FunctionCall.Arguments), &args); err != nil {
		return fmt.Sprintf("invalid arguments: %v", err), nil
	}
	return tool.Call(ctx, args)
}

func truncateToolOutput(output string) string {
	const limit = 16 * 1024
	if len(output) <= limit {
		return output
	}
	return output[:limit] + "\n\n[truncated]"
}

func toolCallStatus(result string) string {
	lower := strings.ToLower(result)
	switch {
	case strings.Contains(lower, "command error:"):
		return "error"
	case strings.Contains(lower, "read failed:"):
		return "error"
	case strings.Contains(lower, "invalid arguments:"):
		return "error"
	case strings.Contains(lower, "unknown tool:"):
		return "error"
	case strings.Contains(lower, "missing function call payload"):
		return "error"
	default:
		return "ok"
	}
}

func logLangchainMessages(messages []llms.MessageContent) {
	payload, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		slog.Warn("langchain_message_log_failed", "error", err)
		return
	}
	slog.Debug("langchain_messages", "payload", string(payload))
}

func logLangchainChoice(choice *llms.ContentChoice) {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	if choice == nil {
		return
	}
	slog.Debug("assistant_response",
		"content", choice.Content,
		"tool_calls", len(choice.ToolCalls),
		"stop_reason", choice.StopReason,
	)
}

func logLangchainToolCall(call llms.ToolCall) {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	if call.FunctionCall == nil {
		return
	}
	slog.Debug("assistant_tool_call",
		"id", call.ID,
		"name", call.FunctionCall.Name,
		"arguments", call.FunctionCall.Arguments,
	)
}

func logLangchainToolResult(call llms.ToolCall, result string) {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	name := ""
	if call.FunctionCall != nil {
		name = call.FunctionCall.Name
	}
	slog.Debug("tool_result",
		"id", call.ID,
		"name", name,
		"content", result,
	)
}

func (transport loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := transport.base
	if base == nil {
		base = http.DefaultTransport
	}

	var bodyText string
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			slog.Warn("langchain_http_request_read_failed", "error", err)
		} else {
			bodyText = string(bodyBytes)
			req.Body = io.NopCloser(strings.NewReader(bodyText))
		}
	}

	slog.Debug("langchain_http_request",
		"method", req.Method,
		"url", req.URL.String(),
		"body", bodyText,
	)

	resp, err := base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func init() {
	chatCmd.Flags().String("backend", "openai", "LangChain backend: openai, anthropic, openai-responses")
	chatCmd.Flags().String("base-url", "", "Provider base URL for the running mclone server")
	chatCmd.Flags().String("api-key", "dummy", "API key to send with LangChain requests")
	chatCmd.Flags().Int("max-iterations", 6, "Maximum LangChain tool-calling rounds")
	chatCmd.Flags().Int("port", 8080, "Default local port when base-url is omitted")
	rootCmd.AddCommand(chatCmd)
}
