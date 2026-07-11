package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/dscsystems/mqtt-console/internal/config"
	"github.com/dscsystems/mqtt-console/internal/decode"
	"github.com/dscsystems/mqtt-console/internal/mqttc"
	"github.com/dscsystems/mqtt-console/internal/store"
)

type browserFocus int

const (
	focusTree browserFocus = iota
	focusDetail
)

type browserModal int

const (
	modalNone browserModal = iota
	modalPublish
	modalSubscribe
	modalSubs
	modalHelp
	modalConfirm
)

type connState int

const (
	stateConnected connState = iota
	stateReconnecting
	stateDisconnected
)

// browserModel is the live topic explorer for one connection.
type browserModel struct {
	client  mqttc.Client
	profile config.Connection
	store   *store.Store
	z       *zone.Manager

	width, height int
	focus         browserFocus
	modal         browserModal

	rows       []*store.Node
	cursor     int
	treeOffset int
	selected   *store.Node

	viewport  viewport.Model
	histIdx   int // 0 = latest, counting backwards
	formats   map[string]decode.Format
	detailHdr string

	filterInput textinput.Model
	filtering   bool
	filter      string

	publishForm   *form
	subscribeForm *form
	subsCursor    int

	confirmPrompt string
	confirmCmd    tea.Cmd

	state    connState
	stateErr string
	paused   bool
	dropped  int // messages skipped while paused
	status   string
	statusAt time.Time
}

func newBrowserModel(client mqttc.Client, profile config.Connection, z *zone.Manager, width, height int) *browserModel {
	fi := textinput.New()
	fi.Prompt = "/"
	fi.Placeholder = "filter topics"
	m := &browserModel{
		client:      client,
		profile:     profile,
		store:       store.New(),
		z:           z,
		formats:     map[string]decode.Format{},
		viewport:    viewport.New(10, 10),
		filterInput: fi,
		state:       stateConnected,
		width:       width,
		height:      height,
	}
	m.layout()
	return m
}

// --- layout ---------------------------------------------------------------

const headerLines = 2
const footerLines = 1

func (m *browserModel) layout() {
	mainH := max(3, m.height-headerLines-footerLines)
	detailW := m.detailWidth()
	m.viewport.Width = max(10, detailW-2)
	m.viewport.Height = max(1, mainH-2-m.detailHeaderLines())
}

func (m *browserModel) treeWidth() int {
	w := m.width * 2 / 5
	if w < 32 {
		w = min(32, m.width/2)
	}
	if w > 70 {
		w = 70
	}
	return w
}

func (m *browserModel) detailWidth() int { return max(20, m.width-m.treeWidth()) }

func (m *browserModel) detailHeaderLines() int { return 4 }

// --- ingest ---------------------------------------------------------------

func (m *browserModel) ingest(batch []mqttc.Message) {
	if m.paused {
		m.dropped += len(batch)
		return
	}
	selTopic := ""
	if m.selected != nil {
		selTopic = m.selected.Path
	}
	refresh := false
	for _, msg := range batch {
		node := m.store.Add(msg)
		if node.Path == selTopic {
			refresh = true
		}
	}
	m.reflatten()
	if refresh && m.histIdx == 0 {
		m.refreshDetail()
	}
}

// reflatten recomputes visible rows, keeping the cursor on the same node.
func (m *browserModel) reflatten() {
	m.rows = m.store.Flatten(m.filter)
	if m.selected != nil {
		for i, n := range m.rows {
			if n == m.selected {
				m.cursor = i
				// Bound-only: don't snap the viewport to the cursor here, or
				// live updates would keep undoing mouse-wheel scrolling.
				m.boundScroll()
				return
			}
		}
	}
	if m.cursor >= len(m.rows) {
		m.cursor = max(0, len(m.rows)-1)
	}
	m.syncSelection()
}

func (m *browserModel) syncSelection() {
	var sel *store.Node
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		sel = m.rows[m.cursor]
	}
	if sel != m.selected {
		m.selected = sel
		m.histIdx = 0
		m.refreshDetail()
	}
	m.clampScroll()
}

func (m *browserModel) clampScroll() {
	visible := m.treeHeight()
	if m.cursor < m.treeOffset {
		m.treeOffset = m.cursor
	}
	if m.cursor >= m.treeOffset+visible {
		m.treeOffset = m.cursor - visible + 1
	}
	if m.treeOffset < 0 {
		m.treeOffset = 0
	}
}

func (m *browserModel) treeHeight() int {
	return max(1, m.height-headerLines-footerLines-2) // minus tree border
}

// --- detail rendering -----------------------------------------------------

func (m *browserModel) currentMessage() *mqttc.Message {
	if m.selected == nil || len(m.selected.History) == 0 {
		return nil
	}
	idx := len(m.selected.History) - 1 - m.histIdx
	if idx < 0 {
		idx = 0
	}
	return &m.selected.History[idx]
}

func (m *browserModel) currentFormat() decode.Format {
	if m.selected == nil {
		return decode.FmtAuto
	}
	return m.formats[m.selected.Path]
}

func (m *browserModel) refreshDetail() {
	msg := m.currentMessage()
	if m.selected == nil {
		m.detailHdr = stDim.Render("no topic selected")
		m.viewport.SetContent("")
		return
	}
	if msg == nil {
		m.detailHdr = stTitle.Render(m.selected.Path) + "\n" +
			stDim.Render(fmt.Sprintf("branch · %d topics · %d messages in subtree",
				m.selected.SubtreeTopics, m.selected.SubtreeMsgs)) + "\n\n"
		m.viewport.SetContent(stDim.Render("(no messages on this exact topic)"))
		m.viewport.GotoTop()
		return
	}

	f := m.currentFormat()
	res := decode.As(f, msg.Payload, decode.Options{
		Topic:            msg.Topic,
		SparkplugAliases: m.store.SparkplugAliases(msg.Topic),
	})

	badges := []string{fmt.Sprintf("QoS %d", msg.QoS)}
	if msg.Retained {
		badges = append(badges, stRetained.Render("retained"))
	}
	if msg.ContentType != "" {
		badges = append(badges, "content-type: "+msg.ContentType)
	}
	fmtLabel := res.Format
	if f == decode.FmtAuto {
		fmtLabel += " (auto)"
	} else {
		fmtLabel += " (forced: " + f.String() + ")"
	}
	line2 := fmt.Sprintf("%s · %s · %s",
		msg.Time.Format("2006-01-02 15:04:05.000"),
		humanBytes(len(msg.Payload)),
		strings.Join(badges, " · "))
	line3 := fmt.Sprintf("format %s · message %d/%d (newest first: [ older, ] newer)",
		stKey.Render(fmtLabel), m.histIdx+1, len(m.selected.History))
	if res.Err != nil {
		line3 += "\n" + stWarn.Render("decode: "+res.Err.Error())
	}

	m.detailHdr = stTitle.Render(m.selected.Path) + "\n" +
		stDim.Render(line2) + "\n" + line3 + "\n"

	body := res.Text
	if len(msg.UserProps) > 0 {
		var props strings.Builder
		props.WriteString(stDim.Render("user properties:") + "\n")
		for _, kv := range msg.UserProps {
			props.WriteString(fmt.Sprintf("  %s = %s\n", kv[0], kv[1]))
		}
		body = props.String() + "\n" + body
	}
	m.viewport.SetContent(body)
	m.viewport.GotoTop()
}

// --- commands -------------------------------------------------------------

// statusMsg is a transient footer notification.
type statusMsg string

func (m *browserModel) publishCmd(topic string, payload []byte, qos byte, retain bool) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Publish(ctx, topic, qos, retain, payload); err != nil {
			return statusMsg("publish failed: " + err.Error())
		}
		return statusMsg(fmt.Sprintf("published to %s (%s, QoS %d%s)",
			topic, humanBytes(len(payload)), qos, map[bool]string{true: ", retained", false: ""}[retain]))
	}
}

func (m *browserModel) subscribeCmd(filter string, qos byte) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Subscribe(ctx, filter, qos); err != nil {
			return statusMsg("subscribe failed: " + err.Error())
		}
		return statusMsg("subscribed to " + filter)
	}
}

func (m *browserModel) unsubscribeCmd(filter string) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Unsubscribe(ctx, filter); err != nil {
			return statusMsg("unsubscribe failed: " + err.Error())
		}
		return statusMsg("unsubscribed from " + filter)
	}
}

// exportCmd writes the selected message to a file in the format shown in the
// detail pane: decoded JSON-like renderings as .json, plain text as .txt, and
// anything that has no valid text form (hex view, failed decode) as raw .bin.
func (m *browserModel) exportCmd() tea.Cmd {
	msg := m.currentMessage()
	if msg == nil {
		return func() tea.Msg { return statusMsg("nothing to export") }
	}
	res := decode.As(m.currentFormat(), msg.Payload, decode.Options{
		Topic:            msg.Topic,
		SparkplugAliases: m.store.SparkplugAliases(msg.Topic),
	})
	data := append([]byte(nil), msg.Payload...)
	ext, kind := "bin", "raw payload"
	if res.Err == nil {
		switch {
		case strings.Contains(res.Format, "JSON"),
			strings.Contains(res.Format, "Sparkplug"),
			strings.Contains(res.Format, "OPC UA"):
			data, ext, kind = []byte(res.Text), "json", res.Format
		case strings.HasPrefix(res.Format, "Text"):
			data, ext, kind = []byte(res.Text), "txt", res.Format
		}
	}
	name := fmt.Sprintf("mqtt-export-%s-%s.%s",
		sanitizeFilename(msg.Topic), time.Now().Format("20060102-150405"), ext)
	return func() tea.Msg {
		if err := os.WriteFile(name, data, 0o644); err != nil {
			return statusMsg("export failed: " + err.Error())
		}
		return statusMsg(fmt.Sprintf("%s written to %s", kind, name))
	}
}

func sanitizeFilename(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return string(out)
}

// --- modals ---------------------------------------------------------------

func (m *browserModel) openPublish(topic string) {
	m.publishForm = newForm(m.z, "Publish",
		newTextField("Topic", topic, "sensors/temp", ""),
		newTextField("Payload", "", `{"value": 42} or @path/to/file`, "prefix with @ to publish a file's contents"),
		newCycleField("QoS", []string{"0", "1", "2"}, 0, ""),
		newToggleField("Retain", false, ""),
	)
	m.modal = modalPublish
}

func (m *browserModel) openSubscribe() {
	m.subscribeForm = newForm(m.z, "Subscribe",
		newTextField("Topic filter", "", "sensors/#", ""),
		newCycleField("QoS", []string{"0", "1", "2"}, 0, ""),
	)
	m.modal = modalSubscribe
}

func (m *browserModel) submitPublish() tea.Cmd {
	f := m.publishForm
	topic := f.value(0)
	if topic == "" {
		f.err = "topic is required"
		return nil
	}
	raw := f.value(1)
	var payload []byte
	if strings.HasPrefix(raw, "@") {
		data, err := os.ReadFile(strings.TrimPrefix(raw, "@"))
		if err != nil {
			f.err = "reading payload file: " + err.Error()
			return nil
		}
		payload = data
	} else {
		payload = []byte(raw)
	}
	qos := byte(f.fields[2].optIdx)
	retain := f.boolValue(3)
	m.modal = modalNone
	return m.publishCmd(topic, payload, qos, retain)
}

// --- update ---------------------------------------------------------------

func (m *browserModel) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return nil
	case statusMsg:
		m.status = string(msg)
		m.statusAt = time.Now()
		return nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return nil
}

// scrollTree moves the tree viewport without changing the selection.
func (m *browserModel) scrollTree(delta int) {
	m.treeOffset += delta
	m.boundScroll()
}

// boundScroll keeps the tree offset within the row range without snapping to
// the cursor (so wheel scrolling is not undone by live updates).
func (m *browserModel) boundScroll() {
	maxOffset := len(m.rows) - m.treeHeight()
	if m.treeOffset > maxOffset {
		m.treeOffset = maxOffset
	}
	if m.treeOffset < 0 {
		m.treeOffset = 0
	}
}

func treeRowZoneID(i int) string { return fmt.Sprintf("browser:row:%d", i) }
func subZoneID(i int) string     { return fmt.Sprintf("browser:sub:%d", i) }

func (m *browserModel) handleMouse(msg tea.MouseMsg) tea.Cmd {
	wheelUp := msg.Button == tea.MouseButtonWheelUp
	wheelDown := msg.Button == tea.MouseButtonWheelDown
	press := msg.Action == tea.MouseActionPress && !wheelUp && !wheelDown

	switch m.modal {
	case modalHelp:
		if press {
			m.modal = modalNone
		}
		return nil
	case modalConfirm:
		// Destructive actions stay keyboard-confirmed.
		return nil
	case modalPublish:
		done, cancel := m.publishForm.updateMouse(msg)
		if cancel {
			m.modal = modalNone
			return nil
		}
		if done {
			return m.submitPublish()
		}
		return nil
	case modalSubscribe:
		done, cancel := m.subscribeForm.updateMouse(msg)
		if cancel {
			m.modal = modalNone
			return nil
		}
		if done {
			f := m.subscribeForm
			filter := f.value(0)
			if filter == "" {
				f.err = "topic filter is required"
				return nil
			}
			m.modal = modalNone
			return m.subscribeCmd(filter, byte(f.fields[1].optIdx))
		}
		return nil
	case modalSubs:
		subs := m.client.Subscriptions()
		switch {
		case wheelUp && m.subsCursor > 0:
			m.subsCursor--
		case wheelDown && m.subsCursor < len(subs)-1:
			m.subsCursor++
		case press && msg.Button == tea.MouseButtonLeft:
			for i := range subs {
				if m.z.Get(subZoneID(i)).InBounds(msg) {
					m.subsCursor = i
					m.modal = modalNone
					return m.unsubscribeCmd(subs[i].Filter)
				}
			}
		}
		return nil
	}

	// Main browser surface.
	switch {
	case wheelUp, wheelDown:
		if m.z.Get("browser:detail").InBounds(msg) {
			if wheelUp {
				m.viewport.LineUp(3)
			} else {
				m.viewport.LineDown(3)
			}
			return nil
		}
		if m.z.Get("browser:tree").InBounds(msg) {
			if wheelUp {
				m.scrollTree(-3)
			} else {
				m.scrollTree(3)
			}
		}
		return nil
	case press && (msg.Button == tea.MouseButtonLeft || msg.Button == tea.MouseButtonRight):
		end := min(len(m.rows), m.treeOffset+m.treeHeight())
		for i := m.treeOffset; i < end; i++ {
			zi := m.z.Get(treeRowZoneID(i))
			if zi.IsZero() || !zi.InBounds(msg) {
				continue
			}
			m.focus = focusTree
			if msg.Button == tea.MouseButtonRight {
				// Right-click: publish to this topic.
				m.cursor = i
				m.syncSelection()
				m.openPublish(m.rows[i].Path)
				return textinput.Blink
			}
			if i == m.cursor {
				// Click on the selected row toggles the branch.
				if m.selected != nil && len(m.selected.Children()) > 0 {
					m.selected.Expanded = !m.selected.Expanded
					m.reflatten()
				}
			} else {
				m.cursor = i
				m.syncSelection()
			}
			return nil
		}
		if m.z.Get("browser:detail").InBounds(msg) {
			m.focus = focusDetail
			return nil
		}
		if m.z.Get("browser:tree").InBounds(msg) {
			m.focus = focusTree
			return nil
		}
	}
	return nil
}

func (m *browserModel) handleKey(key tea.KeyMsg) tea.Cmd {
	// Modal handling first.
	switch m.modal {
	case modalPublish:
		done, cancel, cmd := m.publishForm.update(key)
		if cancel {
			m.modal = modalNone
			return nil
		}
		if done {
			return m.submitPublish()
		}
		return cmd
	case modalSubscribe:
		done, cancel, cmd := m.subscribeForm.update(key)
		if cancel {
			m.modal = modalNone
			return nil
		}
		if done {
			f := m.subscribeForm
			filter := f.value(0)
			if filter == "" {
				f.err = "topic filter is required"
				return nil
			}
			m.modal = modalNone
			return m.subscribeCmd(filter, byte(f.fields[1].optIdx))
		}
		return cmd
	case modalSubs:
		subs := m.client.Subscriptions()
		switch key.String() {
		case "esc", "q", "S":
			m.modal = modalNone
		case "up", "k":
			if m.subsCursor > 0 {
				m.subsCursor--
			}
		case "down", "j":
			if m.subsCursor < len(subs)-1 {
				m.subsCursor++
			}
		case "enter", "u", "d":
			if m.subsCursor < len(subs) {
				f := subs[m.subsCursor].Filter
				m.modal = modalNone
				return m.unsubscribeCmd(f)
			}
		}
		return nil
	case modalHelp:
		m.modal = modalNone
		return nil
	case modalConfirm:
		cmd := m.confirmCmd
		m.confirmCmd = nil
		m.modal = modalNone
		switch key.String() {
		case "y", "Y", "enter":
			return cmd
		}
		return nil
	}

	// Filter input capture.
	if m.filtering {
		switch key.String() {
		case "enter":
			m.filtering = false
			m.filter = m.filterInput.Value()
			m.reflatten()
			return nil
		case "esc":
			m.filtering = false
			m.filterInput.SetValue("")
			m.filter = ""
			m.reflatten()
			return nil
		default:
			var cmd tea.Cmd
			m.filterInput, cmd = m.filterInput.Update(key)
			m.filter = m.filterInput.Value()
			m.reflatten()
			return cmd
		}
	}

	m.status = ""

	// Keys that work regardless of focus.
	switch key.String() {
	case "tab":
		if m.focus == focusTree {
			m.focus = focusDetail
		} else {
			m.focus = focusTree
		}
		return nil
	case "/":
		m.filtering = true
		m.filterInput.Focus()
		return textinput.Blink
	case "p":
		topic := ""
		if m.selected != nil {
			topic = m.selected.Path
		}
		m.openPublish(topic)
		return textinput.Blink
	case "s":
		m.openSubscribe()
		return textinput.Blink
	case "S":
		m.subsCursor = 0
		m.modal = modalSubs
		return nil
	case "f":
		if m.selected != nil {
			cur := m.formats[m.selected.Path]
			for i, f := range decode.Formats {
				if f == cur {
					m.formats[m.selected.Path] = decode.Formats[(i+1)%len(decode.Formats)]
					break
				}
			}
			m.refreshDetail()
		}
		return nil
	case "F":
		if m.selected != nil {
			delete(m.formats, m.selected.Path)
			m.refreshDetail()
		}
		return nil
	case "[":
		if m.selected != nil && m.histIdx < len(m.selected.History)-1 {
			m.histIdx++
			m.refreshDetail()
		}
		return nil
	case "]":
		if m.histIdx > 0 {
			m.histIdx--
			m.refreshDetail()
		}
		return nil
	case "e":
		return m.exportCmd()
	case "z":
		m.paused = !m.paused
		if !m.paused && m.dropped > 0 {
			m.status = fmt.Sprintf("resumed (%d messages skipped while paused)", m.dropped)
			m.dropped = 0
		}
		return nil
	case "R":
		if m.selected != nil && m.selected.HasData() {
			topic := m.selected.Path
			m.confirmPrompt = fmt.Sprintf("Publish empty retained message to clear %s?", topic)
			m.confirmCmd = m.publishCmd(topic, nil, 0, true)
			m.modal = modalConfirm
		}
		return nil
	case "x":
		if m.selected != nil {
			m.store.Prune(m.selected)
			m.selected = nil
			m.reflatten()
			m.syncSelection()
			m.refreshDetail()
		}
		return nil
	case "E":
		m.store.SetExpandedAll(true)
		m.reflatten()
		return nil
	case "C":
		m.store.SetExpandedAll(false)
		m.reflatten()
		return nil
	case "?":
		m.modal = modalHelp
		return nil
	}

	if m.focus == focusDetail {
		var cmd tea.Cmd
		switch key.String() {
		case "g":
			m.viewport.GotoTop()
		case "G":
			m.viewport.GotoBottom()
		default:
			m.viewport, cmd = m.viewport.Update(key)
		}
		return cmd
	}

	// Tree navigation.
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.syncSelection()
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
			m.syncSelection()
		}
	case "pgup":
		m.cursor = max(0, m.cursor-m.treeHeight())
		m.syncSelection()
	case "pgdown":
		m.cursor = min(len(m.rows)-1, m.cursor+m.treeHeight())
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.syncSelection()
	case "g", "home":
		m.cursor = 0
		m.syncSelection()
	case "G", "end":
		m.cursor = max(0, len(m.rows)-1)
		m.syncSelection()
	case "enter", " ":
		if m.selected != nil && len(m.selected.Children()) > 0 {
			m.selected.Expanded = !m.selected.Expanded
			m.reflatten()
		}
	case "right", "l":
		if m.selected != nil && len(m.selected.Children()) > 0 && !m.selected.Expanded {
			m.selected.Expanded = true
			m.reflatten()
		}
	case "left", "h":
		if m.selected != nil {
			if m.selected.Expanded && len(m.selected.Children()) > 0 {
				m.selected.Expanded = false
				m.reflatten()
			} else if m.selected.Parent != nil && m.selected.Parent.Path != "" {
				m.selected = m.selected.Parent
				m.reflatten()
				m.syncSelection()
				m.refreshDetail()
			}
		}
	}
	return nil
}

// --- view -----------------------------------------------------------------

func (m *browserModel) view() string {
	header := m.headerView()
	footer := m.footerView()

	switch m.modal {
	case modalPublish:
		return header + "\n" + centerModal(m.width, m.height-headerLines-footerLines, m.publishForm.view(min(m.width-10, 90))) + "\n" + footer
	case modalSubscribe:
		return header + "\n" + centerModal(m.width, m.height-headerLines-footerLines, m.subscribeForm.view(min(m.width-10, 70))) + "\n" + footer
	case modalSubs:
		return header + "\n" + centerModal(m.width, m.height-headerLines-footerLines, m.subsView()) + "\n" + footer
	case modalHelp:
		return header + "\n" + centerModal(m.width, m.height-headerLines-footerLines, helpView()) + "\n" + footer
	case modalConfirm:
		body := m.confirmPrompt + "\n\n" + stDim.Render("y confirm · any other key cancel")
		return header + "\n" + centerModal(m.width, m.height-headerLines-footerLines, body) + "\n" + footer
	}

	mainH := max(3, m.height-headerLines-footerLines)
	treeStyle, detailStyle := stBorder, stBorder
	if m.focus == focusTree {
		treeStyle = stFocusBorder
	} else {
		detailStyle = stFocusBorder
	}
	tree := m.z.Mark("browser:tree", treeStyle.Width(m.treeWidth()-2).Height(mainH-2).Render(m.treeView()))
	detail := m.z.Mark("browser:detail", detailStyle.Width(m.detailWidth()-2).Height(mainH-2).Render(m.detailView()))
	main := lipgloss.JoinHorizontal(lipgloss.Top, tree, detail)
	return header + "\n" + main + "\n" + footer
}

func (m *browserModel) headerView() string {
	var glyph string
	switch m.state {
	case stateConnected:
		glyph = stGood.Render("● connected")
	case stateReconnecting:
		glyph = stWarn.Render("◌ reconnecting")
	case stateDisconnected:
		glyph = stBad.Render("○ disconnected")
	}
	name := m.profile.Name
	if name == "" {
		name = m.client.ServerURL()
	}
	line1 := fmt.Sprintf("%s  %s  %s  %s",
		stTitle.Render("MQTT Console"),
		stHeader.Render(name),
		stDim.Render(m.client.ServerURL()+" · MQTT "+m.client.ProtocolVersion().String()),
		glyph)
	if m.stateErr != "" && m.state != stateConnected {
		line1 += stDim.Render("  " + m.stateErr)
	}
	if m.paused {
		line1 += "  " + stWarn.Render("⏸ paused")
	}

	subs := m.client.Subscriptions()
	filters := make([]string, 0, len(subs))
	for _, s := range subs {
		filters = append(filters, s.Filter)
	}
	stats := fmt.Sprintf("msgs %d (%.1f/s) · topics %d · %s · subs [%s]",
		m.store.TotalMsgs, m.store.Rate(), m.store.Topics(),
		humanBytes(int(m.store.TotalBytes)), strings.Join(filters, ", "))
	if d := m.client.Dropped(); d > 0 {
		stats += stBad.Render(fmt.Sprintf(" · dropped %d", d))
	}
	line2 := stDim.Render(truncate(stats, m.width))
	return truncate(line1, m.width) + "\n" + line2
}

func (m *browserModel) footerView() string {
	if m.filtering {
		return m.filterInput.View()
	}
	if m.status != "" {
		return stWarn.Render(truncate(m.status, m.width))
	}
	hints := "↑↓ navigate · ⏎ expand · tab focus · / filter · p publish · s subscribe · S subs · f format · [ ] history · e export · z pause · ? help · q back"
	return stDim.Render(truncate(hints, m.width))
}

func (m *browserModel) treeView() string {
	if len(m.rows) == 0 {
		if m.filter != "" {
			return stDim.Render("no topics match the filter")
		}
		return stDim.Render("waiting for messages...")
	}
	h := m.treeHeight()
	end := min(len(m.rows), m.treeOffset+h)
	var b strings.Builder
	width := m.treeWidth() - 2
	for i := m.treeOffset; i < end; i++ {
		n := m.rows[i]
		line := m.treeRow(n, width)
		if i == m.cursor {
			line = stSelected.Render(line)
		}
		b.WriteString(m.z.Mark(treeRowZoneID(i), line))
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m *browserModel) treeRow(n *store.Node, width int) string {
	indent := strings.Repeat("  ", n.Depth-1)
	glyph := "  "
	if len(n.Children()) > 0 {
		if n.Expanded {
			glyph = "▾ "
		} else {
			glyph = "▸ "
		}
	}
	label := n.Name
	var suffix string
	if n.HasData() {
		last := n.Last()
		badge := ""
		if last != nil && last.Retained {
			badge = " ®"
		}
		preview := payloadPreview(n.LastPayload)
		suffix = fmt.Sprintf("%s (%d)%s", preview, n.MsgCount, badge)
	}
	if len(n.Children()) > 0 && !n.HasData() {
		suffix = fmt.Sprintf(" %d topics, %d msgs", n.SubtreeTopics, n.SubtreeMsgs)
	}
	line := indent + glyph + label
	if suffix != "" {
		avail := width - lipgloss.Width(line) - 1
		if avail > 4 {
			line += " " + stDim.Render(truncate(suffix, avail))
		}
	}
	return truncate(line, width)
}

func payloadPreview(p []byte) string {
	if len(p) == 0 {
		return ""
	}
	s := string(p)
	if len(s) > 40 {
		s = s[:40]
	}
	clean := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == 0xfffd {
			return " = 0x…" // binary payload
		}
		clean = append(clean, r)
	}
	return " = " + string(clean)
}

func (m *browserModel) detailView() string {
	return m.detailHdr + "\n" + m.viewport.View()
}

func (m *browserModel) subsView() string {
	subs := m.client.Subscriptions()
	var b strings.Builder
	b.WriteString(stTitle.Render("Active subscriptions") + "\n\n")
	if len(subs) == 0 {
		b.WriteString(stDim.Render("  none") + "\n")
	}
	for i, s := range subs {
		line := fmt.Sprintf("  %-40s QoS %d", s.Filter, s.QoS)
		if i == m.subsCursor {
			line = stSelected.Render("▸ " + line[2:])
		}
		b.WriteString(m.z.Mark(subZoneID(i), line) + "\n")
	}
	b.WriteString("\n" + stDim.Render("enter or click unsubscribe · esc close"))
	return b.String()
}

func helpView() string {
	rows := [][2]string{
		{"↑/↓ j/k", "move in topic tree"},
		{"enter/space", "expand or collapse branch"},
		{"←/→ h/l", "collapse / expand, jump to parent"},
		{"E / C", "expand all / collapse all"},
		{"tab", "switch focus between tree and payload"},
		{"/", "filter topics (esc clears)"},
		{"p", "publish (topic prefilled from selection)"},
		{"s / S", "subscribe / list & unsubscribe"},
		{"f / F", "cycle payload format / reset to auto"},
		{"[ / ]", "older / newer message on this topic"},
		{"e", "export payload as shown (raw .bin when binary)"},
		{"R", "clear retained message (confirm)"},
		{"x", "remove subtree from view"},
		{"z", "pause / resume message capture"},
		{"g/G pgup/pgdn", "jump / page"},
		{"q", "disconnect and return to profiles"},
		{"ctrl+c", "quit"},
		{"", ""},
		{"click", "select topic; click again to expand/collapse"},
		{"right-click", "publish to that topic"},
		{"wheel", "scroll tree or payload under the pointer"},
	}
	var b strings.Builder
	b.WriteString(stTitle.Render("Keys") + "\n\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("  %s  %s\n", stKey.Render(fmt.Sprintf("%-14s", r[0])), r[1]))
	}
	b.WriteString("\n" + stDim.Render("formats: auto → json → text → hex → sparkplug → opcua-uadp → gzip"))
	return b.String()
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	// Truncate by display width, preserving ANSI sequences is complex; rely on
	// lipgloss for measurement and cut runes until it fits.
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)) > w-1 {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
