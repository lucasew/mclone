package server

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"

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
	// ReadHeaderTimeout bounds slowloris-style header stalls.
	// IdleTimeout reclaims keep-alive connections left idle.
	// WriteTimeout is left at 0: chat/responses stream via SSE and can run
	// longer than any short global write deadline.
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}
