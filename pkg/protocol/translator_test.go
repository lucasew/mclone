package protocol

import (
	"testing"

	json "github.com/goccy/go-json"
)

func TestToolToDefinitionAcceptsOpenAIShape(t *testing.T) {
	raw := []byte(`{
		"type":"function",
		"function":{
			"name":"WebSearch",
			"description":"Search the web",
			"parameters":{"type":"object","properties":{"query":{"type":"string"}}}
		}
	}`)

	var tool Tool
	if err := json.Unmarshal(raw, &tool); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}

	def := tool.ToDefinition()
	if def.Type != "function" {
		t.Fatalf("unexpected type: %q", def.Type)
	}
	if def.Name != "WebSearch" {
		t.Fatalf("unexpected name: %q", def.Name)
	}
	if def.Description != "Search the web" {
		t.Fatalf("unexpected description: %q", def.Description)
	}
	if string(def.Parameters) != `{"type":"object","properties":{"query":{"type":"string"}}}` {
		t.Fatalf("unexpected parameters: %s", string(def.Parameters))
	}
}

func TestNormalizeToolArguments(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "{}"},
		{name: "object", in: `{"query":"x"}`, want: `{"query":"x"}`},
		{name: "array", in: `[1,2]`, want: `[1,2]`},
		{name: "encoded object", in: `"{\"query\":\"x\"}"`, want: `{"query":"x"}`},
		{name: "plain string", in: `"hello"`, want: `{"input":"hello"}`},
		{name: "blank string", in: `"   "`, want: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(NormalizeToolArguments(json.RawMessage(tt.in)))
			if got != tt.want {
				t.Fatalf("got %s want %s", got, tt.want)
			}
		})
	}
}

func TestToolToDefinitionKeepsExistingShape(t *testing.T) {
	raw := []byte(`{
		"type":"function",
		"name":"WebFetch",
		"description":"Fetch a page",
		"parameters":{"type":"object","properties":{"url":{"type":"string"}}}
	}`)

	var tool Tool
	if err := json.Unmarshal(raw, &tool); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}

	def := tool.ToDefinition()
	if def.Name != "WebFetch" {
		t.Fatalf("unexpected name: %q", def.Name)
	}
	if string(def.Parameters) != `{"type":"object","properties":{"url":{"type":"string"}}}` {
		t.Fatalf("unexpected parameters: %s", string(def.Parameters))
	}
}
