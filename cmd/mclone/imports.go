package main

import (
	_ "github.com/lucasew/mclone/pkg/providers/anthropic"
	_ "github.com/lucasew/mclone/pkg/providers/antigravity"
	_ "github.com/lucasew/mclone/pkg/providers/balance"
	_ "github.com/lucasew/mclone/pkg/providers/gemini"
	_ "github.com/lucasew/mclone/pkg/providers/ollama"
	_ "github.com/lucasew/mclone/pkg/providers/openai"
	_ "github.com/lucasew/mclone/pkg/providers/route"
	_ "github.com/lucasew/mclone/pkg/providers/toolbox"

	_ "github.com/lucasew/mclone/pkg/tools/duckduckgo"
	_ "github.com/lucasew/mclone/pkg/tools/webfetch"
)
