package chatui

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
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

	transcript []Line
	queue      []string

	input    textinput.Model
	viewport viewport.Model
	spinner  spinner.Model

	activePrompt string
	busy         bool
	err          string
	width        int
	height       int
}

var _ tea.Model = Model{}

func New(ctx context.Context, modelName, backend string, maxIterations int, exchange ExchangeFunc) Model {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "type a message"
	in.Focus()

	vp := viewport.New()
	vp.SoftWrap = true
	vp.FillHeight = true

	return Model{
		ctx:           ctx,
		exchange:      exchange,
		modelName:     modelName,
		backend:       backend,
		maxIterations: maxIterations,
		input:         in,
		viewport:      vp,
		spinner:       spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.input.Focus(), m.spinner.Tick)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			input := strings.TrimSpace(m.input.Value())
			if input == "" {
				return m, nil
			}
			if input == "exit" || input == "quit" {
				return m, tea.Quit
			}
			m.transcript = append(m.transcript, Line{Role: RoleUser, Text: input})
			m.queue = append(m.queue, input)
			m.input.SetValue("")
			m.err = ""
			m.syncViewport()
			if m.busy {
				return m, nil
			}
			return m.startNext()
		}

		var cmds []tea.Cmd
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		m.syncViewport()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case responseMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.transcript = append(m.transcript, msg.lines...)
		}
		m.syncViewport()
		if len(m.queue) > 0 {
			return m.startNext()
		}
		m.busy = false
		m.activePrompt = ""
		return m, nil
	}

	return m, nil
}

func (m Model) View() tea.View {
	width := m.effectiveWidth()
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Reverse(true).
		Padding(0, 1).
		Width(width)
	metaStyle := lipgloss.NewStyle().Faint(true)
	assistantLabelStyle := lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
	toolLabelStyle := lipgloss.NewStyle().Bold(true).Italic(true).Padding(0, 1)
	promptBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(width)

	header := fmt.Sprintf(" mclone chat  model=%s  backend=%s ", m.modelName, m.backend)
	headerBlock := titleStyle.Render(header) + "\n" + metaStyle.Render("enter send  esc quit")

	var statusLines []string
	if m.busy {
		status := m.spinner.View() + " working..."
		if m.activePrompt != "" {
			status = m.spinner.View() + " working on: " + summarizeSingleLine(m.activePrompt, max(width-18, 12))
		}
		statusLines = append(statusLines, lipgloss.JoinHorizontal(
			lipgloss.Top,
			assistantLabelStyle.Render("Agent"),
			" ",
			metaStyle.Render(status),
		))
	}
	if len(m.queue) > 0 {
		next := summarizeSingleLine(m.queue[0], max(width-16, 12))
		statusLines = append(statusLines, lipgloss.JoinHorizontal(
			lipgloss.Top,
			toolLabelStyle.Render("Queue"),
			" ",
			metaStyle.Render(fmt.Sprintf("%d waiting  next: %s", len(m.queue), next)),
		))
	}
	if !m.viewport.AtTop() || !m.viewport.AtBottom() {
		statusLines = append(statusLines, metaStyle.Render("scroll with arrows / pgup / pgdown"))
	}

	inputBlock := promptBoxStyle.Render(m.input.View())
	footerParts := append(statusLines, inputBlock)
	footerBlock := strings.Join(footerParts, "\n")

	mut := m
	mut.resizeForLayout(lipgloss.Height(headerBlock), lipgloss.Height(footerBlock))
	mut.syncViewport()

	var screen strings.Builder
	screen.WriteString(headerBlock)
	screen.WriteString("\n")
	screen.WriteString(mut.viewport.View())
	screen.WriteString("\n")
	screen.WriteString(footerBlock)

	view := tea.NewView(screen.String())
	view.AltScreen = true
	return view
}

func (m Model) startNext() (tea.Model, tea.Cmd) {
	if len(m.queue) == 0 {
		m.busy = false
		m.activePrompt = ""
		return m, nil
	}
	next := m.queue[0]
	m.queue = m.queue[1:]
	m.busy = true
	m.activePrompt = next
	return m, m.runExchange(next)
}

func (m Model) runExchange(prompt string) tea.Cmd {
	return func() tea.Msg {
		lines, err := m.exchange(m.ctx, prompt, m.maxIterations)
		return responseMsg{lines: lines, err: err}
	}
}

func (m *Model) resize() {
	m.resizeForLayout(3, 3)
}

func (m *Model) resizeForLayout(headerHeight, footerHeight int) {
	width := m.effectiveWidth()
	bodyHeight := m.height - headerHeight - footerHeight - 1
	if bodyHeight < 4 {
		bodyHeight = 4
	}
	m.input.SetWidth(max(width-4, 10))
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(bodyHeight)
}

func (m *Model) syncViewport() {
	contentWidth := max(m.effectiveWidth()-6, 20)
	lines := renderTranscript(m.transcript, contentWidth)
	if m.err != "" {
		lines = append(lines, "")
		lines = append(lines, wrapText("error: "+m.err, contentWidth)...)
	}
	m.viewport.SetContent(strings.Join(lines, "\n"))
	m.viewport.GotoBottom()
}

func (m Model) effectiveWidth() int {
	if m.width <= 0 {
		return 88
	}
	return max(m.width-2, 32)
}

func renderTranscript(transcript []Line, width int) []string {
	rendered := make([]string, 0, len(transcript)*2)
	for _, line := range transcript {
		label := "Agent"
		switch line.Role {
		case RoleUser:
			label = "You"
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
