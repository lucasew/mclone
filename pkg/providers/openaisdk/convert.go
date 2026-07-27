// Package openaisdk converts mclone message types to openai-go SDK params.
// Shared by OpenAI-compatible providers (openai, ollama, …).
package openaisdk

import (
	"context"
	"log/slog"

	json "github.com/goccy/go-json"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

// ToMessages maps mclone turns to Chat Completions message params.
// ToolMessage argument order is content, then toolCallID (openai-go API).
func ToMessages(messages []message.Turn) []sdk.ChatCompletionMessageParamUnion {
	var out []sdk.ChatCompletionMessageParamUnion
	for _, m := range messages {
		switch m.Role {
		case message.RoleSystem:
			var textContent string
			for _, p := range m.Parts {
				if tp, ok := p.(message.TextPart); ok {
					textContent += tp.Text
				}
			}
			out = append(out, sdk.SystemMessage(textContent))
		case message.RoleUser:
			var textContent string
			for _, p := range m.Parts {
				switch v := p.(type) {
				case message.TextPart:
					textContent += v.Text
				case message.ToolResultPart:
					out = append(out, sdk.ToolMessage(v.Content, v.ToolCallID))
				}
			}
			if textContent != "" {
				out = append(out, sdk.UserMessage(textContent))
			}
		case message.RoleAssistant:
			var textContent string
			var toolCalls []sdk.ChatCompletionMessageToolCallParam
			for _, p := range m.Parts {
				switch v := p.(type) {
				case message.TextPart:
					textContent += v.Text
				case message.ToolCallPart:
					toolCalls = append(toolCalls, sdk.ChatCompletionMessageToolCallParam{
						ID: v.ID,
						Function: sdk.ChatCompletionMessageToolCallFunctionParam{
							Name:      v.Name,
							Arguments: string(v.Arguments),
						},
					})
				}
			}

			if len(toolCalls) > 0 {
				out = append(out, sdk.ChatCompletionMessageParamUnion{
					OfAssistant: &sdk.ChatCompletionAssistantMessageParam{
						Content: sdk.ChatCompletionAssistantMessageParamContentUnion{
							OfString: sdk.String(textContent),
						},
						ToolCalls: toolCalls,
					},
				})
			} else {
				out = append(out, sdk.AssistantMessage(textContent))
			}
		case message.RoleTool:
			for _, p := range m.Parts {
				if v, ok := p.(message.ToolResultPart); ok {
					out = append(out, sdk.ToolMessage(v.Content, v.ToolCallID))
				}
			}
		}
	}
	return out
}

// ToTools maps tool definitions to Chat Completions tool params.
// reportAction labels monitor.ReportError when parameters fail to unmarshal.
func ToTools(tools []message.ToolDefinition, reportAction string) []sdk.ChatCompletionToolParam {
	var out []sdk.ChatCompletionToolParam
	for _, t := range tools {
		if t.Type != "" && t.Type != "function" {
			slog.Debug("openai_skip_tool", "name", t.Name, "type", t.Type)
			continue
		}
		var params shared.FunctionParameters
		if err := json.Unmarshal(t.Parameters, &params); err != nil {
			monitor.ReportError(context.Background(), err, "action", reportAction, "name", t.Name)
		}

		out = append(out, sdk.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: sdk.String(t.Description),
				Parameters:  params,
			},
		})
	}
	return out
}
