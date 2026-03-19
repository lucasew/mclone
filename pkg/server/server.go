package server

import (
	"log/slog"
	"net/http"
	"net/http/pprof"

	anthropicprotocol "github.com/lucasew/mclone/pkg/protocol/anthropic"
	openaiprotocol "github.com/lucasew/mclone/pkg/protocol/openai"
)

func New(cfg Config) *Server {
	return &Server{
		cfg:             cfg,
		anthropicWriter: anthropicprotocol.NewWriter(),
		openaiWriter:    openaiprotocol.NewWriter(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		s.serveChatRequest(w, r, s.anthropicWriter)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		s.serveChatRequest(w, r, s.openaiWriter)
	})
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			s.serveResponsesRequest(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/v1/responses/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.serveResponsesRetrieve(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		s.serveModels(w, r)
	})

	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request", "method", r.Method, "path", r.URL.Path)
		mux.ServeHTTP(w, r)
	})
	return requestDecompressionMiddleware(handler)
}

func (s *Server) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, s.Handler())
}
