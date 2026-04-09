package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/goccy/go-json"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
)

func (s *Server) serveModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.cfg.Provider.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	resp := struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}{Object: "list", Data: []modelEntry{}}

	for _, m := range models {
		resp.Data = append(resp.Data, modelEntry{
			ID: m.Slug, Object: "model", Created: 1677610602, OwnedBy: "mclone",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		monitor.ReportError(r.Context(), err, "action", "serve_models_encode_error")
	}
}

// ParseGenerationDefaults extracts standard chat generation constraints (temperature, top_p,
// max_tokens, stop sequences) from an arbitrary options map. It is primarily used to
// resolve default constraints specified in server configurations or client overrides, handling
// type coercion for numbers and strings as necessary.
func ParseGenerationDefaults(opts map[string]any) message.ChatOptions {
	var co message.ChatOptions
	if v, ok := opts["temperature"]; ok {
		switch val := v.(type) {
		case float64:
			co.Temperature = &val
		case string:
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				co.Temperature = &f
			}
		}
	}
	if v, ok := opts["top_p"]; ok {
		switch val := v.(type) {
		case float64:
			co.TopP = &val
		case string:
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				co.TopP = &f
			}
		}
	}
	if v, ok := opts["max_tokens"]; ok {
		switch val := v.(type) {
		case int64:
			n := int(val)
			co.MaxTokens = &n
		case float64:
			n := int(val)
			co.MaxTokens = &n
		case string:
			if n, err := strconv.Atoi(val); err == nil {
				co.MaxTokens = &n
			}
		}
	}
	if v, ok := opts["stop"]; ok {
		switch val := v.(type) {
		case string:
			co.Stop = strings.Split(val, ",")
		case []string:
			co.Stop = val
		case []any:
			for _, s := range val {
				if str, ok := s.(string); ok {
					co.Stop = append(co.Stop, str)
				}
			}
		}
	}
	return co
}
