package chatui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Line struct {
	Role Role
	Text string
}

type ExchangeFunc func(ctx context.Context, prompt string, maxIterations int) ([]Line, error)

type responseMsg struct {
	lines []Line
	err   error
}

type Model struct {
	ctx           context.Context
	exchange      ExchangeFunc
	modelName     string
	backend       string
	maxIterations int
	transcript    []Line
	input         string
	busy          bool
	err           string
}

var _ tea.Model = Model{}

func New(ctx context.Context, modelName, backend string, maxIterations int, exchange ExchangeFunc) Model {
	return Model{
		ctx:           ctx,
		modelName:     modelName,
		backend:       backend,
		maxIterations: maxIterations,
		exchange:      exchange,
	}
}

func (Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			if m.busy {
				return m, nil
			}
			input := strings.TrimSpace(m.input)
			if input == "" {
				return m, nil
			}
			if input == "exit" || input == "quit" {
				return m, tea.Quit
			}
			m.transcript = append(m.transcript, Line{Role: RoleUser, Text: input})
			m.input = ""
			m.err = ""
			m.busy = true
			return m, m.runExchange(input)
		case "backspace":
			m.input = trimLastRune(m.input)
			return m, nil
		case "space":
			if !m.busy {
				m.input += " "
			}
			return m, nil
		}
		if !m.busy {
			key := msg.String()
			if len(key) == 1 {
				m.input += key
			}
		}
		return m, nil
	case responseMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.transcript = append(m.transcript, msg.lines...)
		return m, nil
	}
	return m, nil
}

func (m Model) View() tea.View {
	var b strings.Builder
	fmt.Fprintf(&b, "LangChain chat with %s via %s\n\n", m.modelName, m.backend)
	for _, line := range m.transcript {
		switch line.Role {
		case RoleUser:
			fmt.Fprintf(&b, "You: %s\n\n", line.Text)
		case RoleAssistant:
			fmt.Fprintf(&b, "Agent: %s\n\n", line.Text)
		case RoleTool:
			fmt.Fprintf(&b, "Tool: %s\n\n", line.Text)
		}
	}
	if m.err != "" {
		fmt.Fprintf(&b, "Error: %s\n\n", m.err)
	}
	if m.busy {
		b.WriteString("Working...\n")
	} else {
		fmt.Fprintf(&b, "> %s", m.input)
	}
	return tea.NewView(b.String())
}

func (m Model) runExchange(prompt string) tea.Cmd {
	return func() tea.Msg {
		lines, err := m.exchange(m.ctx, prompt, m.maxIterations)
		return responseMsg{
			lines: lines,
			err:   err,
		}
	}
}

func trimLastRune(text string) string {
	if text == "" {
		return text
	}
	runes := []rune(text)
	return string(runes[:len(runes)-1])
}
