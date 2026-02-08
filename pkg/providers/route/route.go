package route

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
)

type modelRoute struct {
	provider  remote.Provider
	modelName string
}

type RouteProvider struct {
	routes map[string]modelRoute
}

func (p *RouteProvider) Name() string { return "route" }

func (p *RouteProvider) List(ctx context.Context) ([]remote.Model, error) {
	var models []remote.Model
	for slug, r := range p.routes {
		name := slug
		if r.modelName != "" {
			name = fmt.Sprintf("%s → %s", slug, r.modelName)
		}
		models = append(models, remote.Model{Slug: slug, Name: name})
	}
	return models, nil
}

func (p *RouteProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	r, ok := p.routes[modelName]
	if !ok {
		return nil, fmt.Errorf("model %q not configured in route", modelName)
	}

	actualModel := modelName
	if r.modelName != "" {
		actualModel = r.modelName
	}

	slog.Info("route_dispatch", "requested", modelName, "actual", actualModel, "provider", r.provider.Name())
	return r.provider.Chat(ctx, actualModel, messages, options)
}

func init() {
	remote.Register("route", func(name string, options map[string]any, resolve remote.Resolver) (remote.Provider, error) {
		if resolve.Provider == nil {
			return nil, fmt.Errorf("route provider requires a resolver")
		}

		routes := make(map[string]modelRoute)
		for model, val := range options {
			target, ok := val.(string)
			if !ok {
				slog.Warn("route_invalid_option", "key", model, "value", val, "expected", "string")
				continue
			}

			parts := strings.SplitN(target, ":", 2)
			remoteName := parts[0]

			prov, err := resolve.Provider(remoteName)
			if err != nil {
				return nil, fmt.Errorf("route %q: failed to resolve remote %q: %w", model, remoteName, err)
			}

			backendModel := ""
			if len(parts) == 2 {
				backendModel = parts[1]
			}

			routes[model] = modelRoute{provider: prov, modelName: backendModel}
		}

		return &RouteProvider{routes: routes}, nil
	})
}
