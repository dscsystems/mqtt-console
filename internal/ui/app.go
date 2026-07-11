// Package ui implements the Bubble Tea terminal user interface.
package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"

	"github.com/dscsystems/mqtt-console/internal/config"
	"github.com/dscsystems/mqtt-console/internal/mqttc"
)

type screen int

const (
	screenConnect screen = iota
	screenConnecting
	screenBrowser
)

// App is the root Bubble Tea model.
type App struct {
	cfg     *config.Config
	z       *zone.Manager
	screen  screen
	connect *connectModel
	browser *browserModel

	width, height int
	pendingName   string // profile being connected to, for the spinner screen

	// autoConnect, when set, is dialled immediately on startup.
	autoConnect *config.Connection
}

// NewApp builds the root model. If auto is non-nil the TUI connects to it
// immediately instead of showing the profile list.
func NewApp(cfg *config.Config, auto *config.Connection) *App {
	z := zone.New()
	return &App{
		cfg:         cfg,
		z:           z,
		connect:     newConnectModel(cfg, z),
		screen:      screenConnect,
		autoConnect: auto,
	}
}

// --- messages -------------------------------------------------------------

type connectResultMsg struct {
	client  mqttc.Client
	profile config.Connection
	err     error
}

type msgBatchMsg []mqttc.Message
type eventMsg mqttc.Event
type pumpClosedMsg struct{}
type tickMsg struct{}

// --- commands -------------------------------------------------------------

func connectCmd(profile config.Connection) tea.Cmd {
	return func() tea.Msg {
		client, err := mqttc.New(profile.ToOptions())
		if err != nil {
			return connectResultMsg{err: err, profile: profile}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.Connect(ctx); err != nil {
			return connectResultMsg{err: err, profile: profile}
		}
		topics := profile.Topics
		if len(topics) == 0 {
			topics = []string{"#"}
		}
		for _, t := range topics {
			if err := client.Subscribe(ctx, t, 0); err != nil {
				client.Disconnect()
				return connectResultMsg{err: err, profile: profile}
			}
		}
		return connectResultMsg{client: client, profile: profile}
	}
}

// listenMessages drains the client channel, batching bursts so the UI update
// loop is not overwhelmed at high message rates.
func listenMessages(ch <-chan mqttc.Message) tea.Cmd {
	return func() tea.Msg {
		m, ok := <-ch
		if !ok {
			return pumpClosedMsg{}
		}
		batch := msgBatchMsg{m}
		for len(batch) < 1024 {
			select {
			case m2, ok := <-ch:
				if !ok {
					return batch
				}
				batch = append(batch, m2)
			default:
				return batch
			}
		}
		return batch
	}
}

func listenEvents(ch <-chan mqttc.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return pumpClosedMsg{}
		}
		return eventMsg(e)
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// --- tea.Model ------------------------------------------------------------

// Init implements tea.Model.
func (a *App) Init() tea.Cmd {
	if a.autoConnect != nil {
		c := *a.autoConnect
		a.autoConnect = nil
		a.screen = screenConnecting
		a.pendingName = displayName(c)
		return connectCmd(c)
	}
	return nil
}

func displayName(c config.Connection) string {
	if c.Name != "" {
		return c.Name
	}
	return c.URL
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Headless or exotic terminals can report a zero size; fall back to a
		// classic 80x24 so the UI stays usable.
		if msg.Width <= 0 {
			msg.Width = 80
		}
		if msg.Height <= 0 {
			msg.Height = 24
		}
		a.width, a.height = msg.Width, msg.Height
		a.connect.update(msg)
		if a.browser != nil {
			a.browser.update(msg)
		}
		return a, nil

	case tea.MouseMsg:
		switch a.screen {
		case screenConnect:
			cmd, chosen := a.connect.update(msg)
			if chosen != nil {
				a.screen = screenConnecting
				a.pendingName = displayName(*chosen)
				return a, connectCmd(*chosen)
			}
			return a, cmd
		case screenBrowser:
			if a.browser != nil {
				return a, a.browser.update(msg)
			}
		}
		return a, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			a.shutdown()
			return a, tea.Quit
		}
		switch a.screen {
		case screenConnect:
			if msg.String() == "q" && a.connect.mode == connectList {
				return a, tea.Quit
			}
			cmd, chosen := a.connect.update(msg)
			if chosen != nil {
				a.screen = screenConnecting
				a.pendingName = displayName(*chosen)
				return a, connectCmd(*chosen)
			}
			return a, cmd
		case screenConnecting:
			if msg.String() == "esc" || msg.String() == "q" {
				a.screen = screenConnect
			}
			return a, nil
		case screenBrowser:
			if msg.String() == "q" && a.browser.modal == modalNone && !a.browser.filtering && a.browser.focus == focusTree {
				a.shutdown()
				a.screen = screenConnect
				a.connect.status = ""
				return a, nil
			}
			return a, a.browser.update(msg)
		}
		return a, nil

	case connectResultMsg:
		if a.screen != screenConnecting {
			// User cancelled while dialling; drop the late connection.
			if msg.client != nil {
				msg.client.Disconnect()
			}
			return a, nil
		}
		if msg.err != nil {
			a.screen = screenConnect
			a.connect.status = "connection failed: " + msg.err.Error()
			return a, nil
		}
		a.browser = newBrowserModel(msg.client, msg.profile, a.z, a.width, a.height)
		a.screen = screenBrowser
		return a, tea.Batch(
			listenMessages(msg.client.Messages()),
			listenEvents(msg.client.Events()),
			tickCmd(),
		)

	case msgBatchMsg:
		if a.browser == nil {
			return a, nil
		}
		a.browser.ingest(msg)
		return a, listenMessages(a.browser.client.Messages())

	case eventMsg:
		if a.browser == nil {
			return a, nil
		}
		e := mqttc.Event(msg)
		switch e.Kind {
		case mqttc.EventConnected:
			a.browser.state = stateConnected
			a.browser.stateErr = ""
		case mqttc.EventReconnecting:
			a.browser.state = stateReconnecting
			if e.Err != nil {
				a.browser.stateErr = e.Err.Error()
			}
		case mqttc.EventDisconnected:
			a.browser.state = stateDisconnected
			if e.Err != nil {
				a.browser.stateErr = e.Err.Error()
			}
		}
		return a, listenEvents(a.browser.client.Events())

	case pumpClosedMsg:
		return a, nil

	case tickMsg:
		if a.screen != screenBrowser {
			return a, nil
		}
		// Periodic re-render keeps the rate and clock fresh.
		return a, tickCmd()

	case statusMsg:
		if a.browser != nil {
			return a, a.browser.update(msg)
		}
		return a, nil
	}

	// Forward everything else (e.g. cursor blink) to the active screen.
	switch a.screen {
	case screenConnect:
		cmd, _ := a.connect.update(msg)
		return a, cmd
	case screenBrowser:
		if a.browser != nil {
			return a, a.browser.update(msg)
		}
	}
	return a, nil
}

func (a *App) shutdown() {
	if a.browser != nil {
		a.browser.client.Disconnect()
		a.browser = nil
	}
}

// View implements tea.Model.
func (a *App) View() string {
	var v string
	switch a.screen {
	case screenConnecting:
		v = centerModal(a.width, a.height,
			"Connecting to "+stTitle.Render(a.pendingName)+" ...\n\n"+
				stDim.Render("esc cancel"))
	case screenBrowser:
		if a.browser != nil {
			v = a.browser.view()
		}
	default:
		v = a.connect.view()
	}
	if v == "" {
		v = a.connect.view()
	}
	// Scan registers the positions of all marked zones for mouse hit-testing
	// and strips the markers from the rendered output.
	return a.z.Scan(v)
}
