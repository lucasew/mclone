package main

import (
	_ "github.com/lucasew/mclone/pkg/providers/anthropic"
	_ "github.com/lucasew/mclone/pkg/providers/balance"
	_ "github.com/lucasew/mclone/pkg/providers/gemini"
	_ "github.com/lucasew/mclone/pkg/providers/geminioauth"
	_ "github.com/lucasew/mclone/pkg/providers/ollama"
	_ "github.com/lucasew/mclone/pkg/providers/openai"
	_ "github.com/lucasew/mclone/pkg/providers/route"
	_ "github.com/lucasew/mclone/pkg/providers/toolbox"

	_ "github.com/lucasew/mclone/pkg/search/ddg"
	_ "github.com/lucasew/mclone/pkg/search/stub"

	_ "github.com/lucasew/mclone/pkg/tools/webfetch"
	_ "github.com/lucasew/mclone/pkg/tools/websearch"
)
