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
	"charm.land/glamour/v2"
	lipgloss "charm.land/lipgloss/v2"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Line struct {
	ID     string
	Role   Role
	Text   string
	Detail string
	Status string
}

const PendingAssistantID = "__assistant_pending__"

type ExchangeFunc func(ctx context.Context, prompt string, maxIterations int, emit func(Line)) error

type responseMsg struct {
	err   error
}

type LineMsg struct {
	Line Line
}

type Model struct {
	ctx           context.Context
	exchange      ExchangeFunc
	modelName     string
	backend       string
	maxIterations int
	finalSeq      int

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
		follow := m.viewport.AtBottom()
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.busy {
			m.syncViewportWithFollow(follow)
		}
		return m, cmd

	case responseMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
		}
		m.removePendingPlaceholder()
		m.syncViewport()
		if len(m.queue) > 0 {
			return m.startNext()
		}
		m.busy = false
		m.activePrompt = ""
		return m, nil
	case LineMsg:
		follow := m.viewport.AtBottom()
		m.upsertLine(msg.Line)
		m.syncViewportWithFollow(follow)
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
	toolLabelStyle := lipgloss.NewStyle().Bold(true).Italic(true).Padding(0, 1)
	promptBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(width)

	header := fmt.Sprintf(" mclone chat  model=%s  backend=%s ", m.modelName, m.backend)
	headerBlock := titleStyle.Render(header) + "\n" + metaStyle.Render("enter send  esc quit")

	var statusLines []string
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
	m.upsertLine(Line{
		ID:     PendingAssistantID,
		Role:   RoleAssistant,
		Text:   "",
		Status: "running",
	})
	m.syncViewport()
	return m, m.runExchange(next)
}

func (m Model) runExchange(prompt string) tea.Cmd {
	return func() tea.Msg {
		err := m.exchange(m.ctx, prompt, m.maxIterations, func(Line) {})
		return responseMsg{err: err}
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
	m.syncViewportWithFollow(true)
}

func (m *Model) syncViewportWithFollow(follow bool) {
	contentWidth := max(m.effectiveWidth()-6, 20)
	lines := renderTranscript(m.transcript, contentWidth, m.spinner.View())
	if m.err != "" {
		lines = append(lines, "")
		lines = append(lines, wrapText("error: "+m.err, contentWidth)...)
	}
	m.viewport.SetContent(strings.Join(lines, "\n"))
	if follow {
		m.viewport.GotoBottom()
	}
}

func (m Model) effectiveWidth() int {
	if m.width <= 0 {
		return 88
	}
	return max(m.width-2, 32)
}

func renderTranscript(transcript []Line, width int, spinnerFrame string) []string {
	containerWidth := min(width, 96)
	bubbleMaxWidth := max((containerWidth*4)/5, 16)
	userBubbleStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		MaxWidth(bubbleMaxWidth)
	assistantBubbleStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		MaxWidth(bubbleMaxWidth)
	toolLineStyle := lipgloss.NewStyle().
		Faint(true).
		Italic(true)

	rendered := make([]string, 0, len(transcript)*4)
	for _, line := range transcript {
		bubbleStyle := assistantBubbleStyle
		align := lipgloss.Left
		switch line.Role {
		case RoleUser:
			bubbleStyle = userBubbleStyle
			align = lipgloss.Right
		case RoleTool:
			toolText := strings.TrimSpace(line.Text)
			prefix := ""
			if line.Status != "" {
				if line.Status == "running" {
					prefix = spinnerFrame
				} else {
					prefix = toolStatusIcon(line.Status)
				}
			}
			if prefix != "" {
				toolText = prefix + " " + toolText
			}
			if line.Detail != "" {
				toolText += " " + strings.TrimSpace(line.Detail)
			}
			for _, part := range wrapText(toolText, max(containerWidth-12, 12)) {
				content := toolLineStyle.Render(part)
				centered := lipgloss.NewStyle().Width(containerWidth).Align(lipgloss.Center).Render(content)
				block := lipgloss.PlaceHorizontal(width, lipgloss.Center, centered)
				rendered = append(rendered, strings.Split(block, "\n")...)
			}
			rendered = append(rendered, "")
			continue
		}

		if line.Status == "running" {
			text := strings.TrimSpace(line.Text)
			if text == "" {
				text = spinnerFrame
			} else {
				text += " " + spinnerFrame
			}
			bubble := bubbleStyle.Width(bubbleMaxWidth).Render(renderMarkdownText(text, max(bubbleMaxWidth-4, 12)))
			row := lipgloss.NewStyle().Width(containerWidth).Align(align).Render(bubble)
			rendered = append(rendered, strings.Split(lipgloss.PlaceHorizontal(width, lipgloss.Center, row), "\n")...)
			rendered = append(rendered, "")
			continue
		}

		bubble := bubbleStyle.Width(bubbleMaxWidth).Render(renderMarkdownText(strings.TrimSpace(line.Text), max(bubbleMaxWidth-4, 12)))
		row := lipgloss.NewStyle().Width(containerWidth).Align(align).Render(bubble)
		rendered = append(rendered, strings.Split(lipgloss.PlaceHorizontal(width, lipgloss.Center, row), "\n")...)
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

func renderMarkdownText(text string, width int) string {
	if text == "" {
		return ""
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithEnvironmentConfig(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return strings.Join(wrapText(text, width), "\n")
	}
	out, err := renderer.Render(text)
	if err != nil {
		return strings.Join(wrapText(text, width), "\n")
	}
	return strings.Trim(out, "\n")
}

func (m *Model) upsertLine(line Line) {
	if line.ID == "" {
		if line.Role != RoleAssistant && m.hasLineID(PendingAssistantID) {
			m.insertBeforePending(line)
			return
		}
		m.transcript = append(m.transcript, line)
		return
	}
	if line.Text == "" && line.Detail == "" && line.Status == "" {
		m.removeLineByID(line.ID)
		return
	}
	for i := range m.transcript {
		if m.transcript[i].ID == line.ID {
			m.transcript[i] = line
			return
		}
	}
	if line.ID != PendingAssistantID && m.hasLineID(PendingAssistantID) {
		m.insertBeforePending(line)
		return
	}
	m.transcript = append(m.transcript, line)
}

func (m *Model) removeLineByID(id string) {
	if id == "" {
		return
	}
	filtered := m.transcript[:0]
	for _, line := range m.transcript {
		if line.ID == id {
			continue
		}
		filtered = append(filtered, line)
	}
	m.transcript = filtered
}

func (m *Model) removePendingPlaceholder() {
	for i, line := range m.transcript {
		if line.ID != PendingAssistantID {
			continue
		}
		if strings.TrimSpace(line.Text) == "" && line.Status == "running" {
			m.transcript = append(m.transcript[:i], m.transcript[i+1:]...)
			return
		}
		m.transcript[i].Status = ""
		m.finalSeq++
		m.transcript[i].ID = fmt.Sprintf("assistant-final:%d", m.finalSeq)
		return
	}
}

func (m *Model) hasLineID(id string) bool {
	for _, line := range m.transcript {
		if line.ID == id {
			return true
		}
	}
	return false
}

func (m *Model) insertBeforePending(line Line) {
	for i := range m.transcript {
		if m.transcript[i].ID == PendingAssistantID {
			m.transcript = append(m.transcript[:i], append([]Line{line}, m.transcript[i:]...)...)
			return
		}
	}
	m.transcript = append(m.transcript, line)
}

func toolStatusIcon(status string) string {
	switch status {
	case "ok":
		return "*"
	case "error":
		return "!"
	default:
		return status
	}
}
