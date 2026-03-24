package chatui

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
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
	width         int
	height        int
	scroll        int
	queue         []string
	activePrompt  string
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
		case "up", "k":
			if m.scroll > 0 {
				m.scroll--
			}
			return m, nil
		case "down", "j":
			m.scroll++
			return m, nil
		case "pgup":
			m.scroll -= 5
			if m.scroll < 0 {
				m.scroll = 0
			}
			return m, nil
		case "pgdown":
			m.scroll += 5
			return m, nil
		case "enter":
			input := strings.TrimSpace(m.input)
			if input == "" {
				return m, nil
			}
			if input == "exit" || input == "quit" {
				return m, tea.Quit
			}
			m.transcript = append(m.transcript, Line{Role: RoleUser, Text: input})
			m.queue = append(m.queue, input)
			m.input = ""
			m.err = ""
			m.scroll = 1 << 30
			if m.busy {
				return m, nil
			}
			m.busy = true
			next := m.popQueue()
			m.activePrompt = next
			return m, m.runExchange(next)
		case "backspace", "ctrl+h":
			m.input = trimLastRune(m.input)
			return m, nil
		case "space":
			m.input += " "
			return m, nil
		}
		if text := msg.Key().Text; text != "" {
			m.input += text
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case responseMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			m.scroll = 1 << 30
		} else {
			m.transcript = append(m.transcript, msg.lines...)
			m.scroll = 1 << 30
		}
		if len(m.queue) > 0 {
			next := m.popQueue()
			m.busy = true
			m.activePrompt = next
			return m, m.runExchange(next)
		}
		m.busy = false
		m.activePrompt = ""
		return m, nil
	}
	return m, nil
}

func (m Model) View() tea.View {
	width := m.width
	if width <= 0 {
		width = 88
	}
	outerWidth := max(width-2, 32)
	contentWidth := max(outerWidth-6, 20)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Reverse(true).
		Padding(0, 1).
		Width(outerWidth)
	metaStyle := lipgloss.NewStyle().
		Faint(true)
	userLabelStyle := lipgloss.NewStyle().
		Bold(true).
		Underline(true).
		Padding(0, 1)
	assistantLabelStyle := lipgloss.NewStyle().
		Bold(true).
		Reverse(true).
		Padding(0, 1)
	toolLabelStyle := lipgloss.NewStyle().
		Bold(true).
		Italic(true).
		Padding(0, 1)
	lineBodyStyle := lipgloss.NewStyle()
	errorStyle := lipgloss.NewStyle().
		Bold(true).
		Underline(true)
	promptBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(outerWidth)

	header := fmt.Sprintf(" mclone chat  model=%s  backend=%s ", m.modelName, m.backend)
	lines := renderTranscript(m.transcript, contentWidth)
	headerBlock := titleStyle.Render(header) + "\n" + metaStyle.Render("enter send  esc quit")

	promptText := promptBoxStyle.Render(m.input + "█")

	var footerParts []string
	if m.busy {
		status := "working..."
		if m.activePrompt != "" {
			status = "working on: " + summarizeSingleLine(m.activePrompt, max(outerWidth-14, 12))
		}
		footerParts = append(footerParts, lipgloss.JoinHorizontal(
			lipgloss.Top,
			assistantLabelStyle.Render("Agent"),
			" ",
			metaStyle.Render(status),
		))
	}
	if len(m.queue) > 0 {
		next := summarizeSingleLine(m.queue[0], max(outerWidth-16, 12))
		footerParts = append(footerParts, lipgloss.JoinHorizontal(
			lipgloss.Top,
			toolLabelStyle.Render("Queue"),
			" ",
			metaStyle.Render(fmt.Sprintf("%d waiting  next: %s", len(m.queue), next)),
		))
	}
	footerParts = append(footerParts, promptText)
	footerBlock := strings.Join(footerParts, "\n")
	maxScrollHint := 0
	bodyHeight := m.height - lipgloss.Height(headerBlock) - 1 - lipgloss.Height(footerBlock)
	if bodyHeight < 4 {
		bodyHeight = 4
	}

	if len(lines) > bodyHeight {
		maxScrollHint = len(lines) - bodyHeight
	}
	if m.scroll > maxScrollHint {
		m.scroll = maxScrollHint
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	start := m.scroll
	end := start + bodyHeight
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		start = end
	}
	lines = lines[start:end]
	if len(lines) < bodyHeight {
		pad := make([]string, bodyHeight-len(lines))
		lines = append(lines, pad...)
	}

	var transcript strings.Builder
	for _, line := range lines {
		transcript.WriteString(renderStyledLine(line, lineBodyStyle, userLabelStyle, assistantLabelStyle, toolLabelStyle, contentWidth))
		transcript.WriteString("\n")
	}
	if m.err != "" {
		for _, line := range wrapText("error: "+m.err, max(width-2, 20)) {
			transcript.WriteString(errorStyle.Render(line))
			transcript.WriteString("\n")
		}
	}
	if maxScrollHint > 0 {
		footerParts = append(footerParts[:len(footerParts)-1], append([]string{
			metaStyle.Render(fmt.Sprintf("scroll %d/%d  up/down move  pgup/pgdown jump", m.scroll, maxScrollHint)),
		}, footerParts[len(footerParts)-1])...)
		footerBlock = strings.Join(footerParts, "\n")
	}

	bodyBlock := transcript.String()
	var screen strings.Builder
	screen.WriteString(headerBlock)
	screen.WriteString("\n")
	screen.WriteString(bodyBlock)
	screen.WriteString("\n")
	screen.WriteString(footerBlock)
	view := tea.NewView(screen.String())
	view.AltScreen = true
	return view
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

func (m *Model) popQueue() string {
	if len(m.queue) == 0 {
		return ""
	}
	next := m.queue[0]
	m.queue = m.queue[1:]
	return next
}

func trimLastRune(text string) string {
	if text == "" {
		return text
	}
	runes := []rune(text)
	return string(runes[:len(runes)-1])
}

func renderTranscript(transcript []Line, width int) []string {
	rendered := make([]string, 0, len(transcript)*2)
	for _, line := range transcript {
		label := "Agent"
		switch line.Role {
		case RoleUser:
			label = "You"
		case RoleAssistant:
			label = "Agent"
		case RoleTool:
			label = "Tool"
		}
		prefix := label + " "
		wrapped := wrapText(line.Text, max(width-len(label)-2, 10))
		if len(wrapped) == 0 {
			rendered = append(rendered, prefix)
			rendered = append(rendered, "")
			continue
		}
		rendered = append(rendered, prefix+wrapped[0])
		for _, cont := range wrapped[1:] {
			rendered = append(rendered, strings.Repeat(" ", len(prefix))+cont)
		}
		rendered = append(rendered, "")
	}
	return rendered
}

func renderStyledLine(line string, bodyStyle, userLabelStyle, assistantLabelStyle, toolLabelStyle lipgloss.Style, width int) string {
	labelText := ""
	content := line
	if idx := strings.Index(line, " "); idx > 0 {
		labelText = line[:idx]
		content = strings.TrimLeft(line[idx+1:], " ")
	}

	labelStyle := assistantLabelStyle
	switch labelText {
	case "You":
		labelStyle = userLabelStyle
	case "Tool":
		labelStyle = toolLabelStyle
	case "":
		return bodyStyle.Render(line)
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		labelStyle.Render(labelText),
		" ",
		bodyStyle.Width(max(width-lipgloss.Width(labelText)-3, 10)).Render(content),
	)
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	paras := strings.Split(text, "\n")
	lines := make([]string, 0, len(paras))
	for _, para := range paras {
		if para == "" {
			lines = append(lines, "")
			continue
		}
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		current := words[0]
		for _, word := range words[1:] {
			if utf8.RuneCountInString(current)+1+utf8.RuneCountInString(word) > width {
				lines = append(lines, current)
				current = word
				continue
			}
			current += " " + word
		}
		lines = append(lines, current)
	}
	return lines
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func summarizeSingleLine(text string, width int) string {
	text = strings.Join(strings.Fields(text), " ")
	if width <= 0 || utf8.RuneCountInString(text) <= width {
		return text
	}
	runes := []rune(text)
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}
