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
		models = append(models, remote.Model{
			Slug:    slug,
			Name:    name,
			OwnedBy: []string{r.provider.Name()},
		})
	}
	return models, nil
}

func (p *RouteProvider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	r, ok := p.routes[req.Model]
	if !ok {
		// If exact match not found, maybe try "default" route?
		// For now, fail.
		return nil, fmt.Errorf("model %q not configured in route", req.Model)
	}

	actualModel := req.Model
	if r.modelName != "" {
		actualModel = r.modelName
	}
	// If the route was just "provider", modelName stays "modelName"
	// If the route was "provider:model", modelName becomes "model"

	// Wait, the logic below:
	// target := "remoteName:backendModel"
	// remoteName := parts[0]
	// backendModel := parts[1] (optional)

	// If backendModel is "", it means we forward the modelName as is?
	// The original code:
	/*
		if r.modelName != "" {
			actualModel = r.modelName
		}
	*/
	// This means if I map "gpt4" -> "openai", r.modelName is empty.
	// So actualModel is "gpt4".
	// But "openai" provider expects "gpt-4" or whatever.
	// So usually "gpt4" -> "openai:gpt-4".

	// Wait, if I have:
	// [remotes.myroute]
	// type = "route"
	// [remotes.myroute.options]
	// "gpt-4" = "openai:gpt-4"
	// "claude" = "anthropic:claude-3-opus-20240229"

	slog.Info("route_dispatch", "requested", req.Model, "actual", actualModel, "provider", r.provider.Name())
	req.Model = actualModel
	return r.provider.Chat(ctx, req)
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

			remoteName, rest, found := strings.Cut(target, ":")
			backendModel := ""
			if found {
				backendModel = rest
			}

			prov, err := resolve.Provider(remoteName)
			if err != nil {
				return nil, fmt.Errorf("route %q: failed to resolve remote %q: %w", model, remoteName, err)
			}

			routes[model] = modelRoute{provider: prov, modelName: backendModel}
		}

		return &RouteProvider{routes: routes}, nil
	})
}
