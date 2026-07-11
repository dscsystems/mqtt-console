package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"

	"github.com/dscsystems/mqtt-console/internal/config"
	"github.com/dscsystems/mqtt-console/internal/mqttc"
)

type connectMode int

const (
	connectList connectMode = iota
	connectForm
	connectConfirmDelete
)

// connectModel is the profile manager / connection screen.
type connectModel struct {
	cfg     *config.Config
	z       *zone.Manager
	mode    connectMode
	cursor  int
	form    *form
	editing string // profile name being edited ("" = new)
	status  string
	width   int
	height  int
}

func newConnectModel(cfg *config.Config, z *zone.Manager) *connectModel {
	return &connectModel{cfg: cfg, z: z}
}

// connectRequestMsg asks the app to open a connection.
type connectRequestMsg struct {
	conn config.Connection
}

var versionOptions = []string{"3.1.1", "5.0", "3.1"}

func versionOptionIndex(v int) int {
	switch v {
	case 5:
		return 1
	case 3:
		return 2
	default:
		return 0
	}
}

func versionFromOption(s string) int {
	switch s {
	case "5.0":
		return 5
	case "3.1":
		return 3
	default:
		return 4
	}
}

func (m *connectModel) openForm(c config.Connection, editing string) {
	topics := strings.Join(c.Topics, ", ")
	if topics == "" {
		topics = "#"
	}
	keepAlive := ""
	if c.KeepAlive > 0 {
		keepAlive = strconv.Itoa(c.KeepAlive)
	}
	m.form = newForm(m.z, "Connection",
		newTextField("Name", c.Name, "my-broker", "profile name; leave empty to connect without saving"),
		newTextField("Broker URL", c.URL, "mqtt://test.mosquitto.org:1883", "schemes: tcp ssl mqtt mqtts ws wss (default port added)"),
		newCycleField("MQTT version", versionOptions, versionOptionIndex(c.Version), ""),
		newTextField("Client ID", c.ClientID, "(auto-generated)", ""),
		newTextField("Username", c.Username, "", "supports ${ENV_VAR} expansion"),
		newSecretField("Password", c.Password, "supports ${ENV_VAR} expansion"),
		newTextField("CA file", c.TLS.CA, "", "PEM file with broker CA certificate"),
		newTextField("Client cert", c.TLS.Cert, "", "PEM client certificate (mutual TLS)"),
		newTextField("Client key", c.TLS.Key, "", "PEM client private key"),
		newToggleField("Skip TLS verify", c.TLS.Insecure, "accept any broker certificate (insecure)"),
		newTextField("Keep-alive (s)", keepAlive, "30", ""),
		newTextField("Subscribe", topics, "#", "comma-separated topic filters subscribed on connect"),
	)
	m.editing = editing
	m.mode = connectForm
}

func (m *connectModel) formConnection() (config.Connection, error) {
	f := m.form
	c := config.Connection{
		Name:     f.value(0),
		URL:      f.value(1),
		Version:  versionFromOption(f.value(2)),
		ClientID: f.value(3),
		Username: f.value(4),
		Password: f.value(5),
		TLS: config.TLS{
			CA:       f.value(6),
			Cert:     f.value(7),
			Key:      f.value(8),
			Insecure: f.boolValue(9),
		},
	}
	if c.URL == "" {
		return c, fmt.Errorf("broker URL is required")
	}
	if ka := f.value(10); ka != "" {
		n, err := strconv.Atoi(ka)
		if err != nil || n < 0 {
			return c, fmt.Errorf("keep-alive must be a number of seconds")
		}
		c.KeepAlive = n
	}
	for _, t := range strings.Split(f.value(11), ",") {
		if t = strings.TrimSpace(t); t != "" {
			c.Topics = append(c.Topics, t)
		}
	}
	return c, nil
}

// submitForm validates the form, saves named profiles and returns the
// connection to dial, or nil when validation failed.
func (m *connectModel) submitForm() *config.Connection {
	c, err := m.formConnection()
	if err != nil {
		m.form.err = err.Error()
		return nil
	}
	if c.Name != "" {
		if m.editing != "" && m.editing != c.Name {
			m.cfg.Delete(m.editing)
		}
		m.cfg.Upsert(c)
		if err := m.cfg.Save(); err != nil {
			m.form.err = "saving config: " + err.Error()
			return nil
		}
	}
	m.mode = connectList
	return &c
}

func (m *connectModel) update(msg tea.Msg) (tea.Cmd, *config.Connection) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return nil, nil
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.KeyMsg:
		switch m.mode {
		case connectForm:
			done, cancel, cmd := m.form.update(msg)
			if cancel {
				m.mode = connectList
				return nil, nil
			}
			if done {
				return nil, m.submitForm()
			}
			return cmd, nil
		case connectConfirmDelete:
			switch msg.String() {
			case "y", "Y", "enter":
				if m.cursor < len(m.cfg.Connections) {
					m.cfg.Delete(m.cfg.Connections[m.cursor].Name)
					_ = m.cfg.Save()
					if m.cursor >= len(m.cfg.Connections) && m.cursor > 0 {
						m.cursor--
					}
				}
				m.mode = connectList
			default:
				m.mode = connectList
			}
			return nil, nil
		}
		// List mode.
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.cfg.Connections)-1 {
				m.cursor++
			}
		case "enter":
			if m.cursor < len(m.cfg.Connections) {
				c := m.cfg.Connections[m.cursor]
				return nil, &c
			}
		case "n", "c":
			m.openForm(config.Connection{}, "")
		case "e":
			if m.cursor < len(m.cfg.Connections) {
				c := m.cfg.Connections[m.cursor]
				m.openForm(c, c.Name)
			}
		case "d":
			if m.cursor < len(m.cfg.Connections) {
				m.mode = connectConfirmDelete
			}
		}
	}
	return nil, nil
}

func (m *connectModel) updateMouse(msg tea.MouseMsg) (tea.Cmd, *config.Connection) {
	switch m.mode {
	case connectForm:
		done, cancel := m.form.updateMouse(msg)
		if cancel {
			m.mode = connectList
			return nil, nil
		}
		if done {
			return nil, m.submitForm()
		}
		return nil, nil
	case connectConfirmDelete:
		// Clicking anywhere cancels; deletion stays keyboard-confirmed.
		if msg.Action == tea.MouseActionPress {
			m.mode = connectList
		}
		return nil, nil
	}

	// List mode.
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.cursor > 0 {
			m.cursor--
		}
		return nil, nil
	case tea.MouseButtonWheelDown:
		if m.cursor < len(m.cfg.Connections)-1 {
			m.cursor++
		}
		return nil, nil
	}
	if msg.Action != tea.MouseActionPress {
		return nil, nil
	}
	if msg.Button == tea.MouseButtonLeft {
		switch {
		case m.z.Get("connect:new").InBounds(msg):
			m.openForm(config.Connection{}, "")
			return nil, nil
		case m.z.Get("connect:edit").InBounds(msg):
			if m.cursor < len(m.cfg.Connections) {
				c := m.cfg.Connections[m.cursor]
				m.openForm(c, c.Name)
			}
			return nil, nil
		case m.z.Get("connect:delete").InBounds(msg):
			if m.cursor < len(m.cfg.Connections) {
				m.mode = connectConfirmDelete
			}
			return nil, nil
		}
	}
	for i := range m.cfg.Connections {
		if !m.z.Get(profileZoneID(i)).InBounds(msg) {
			continue
		}
		if msg.Button == tea.MouseButtonRight {
			c := m.cfg.Connections[i]
			m.cursor = i
			m.openForm(c, c.Name)
			return nil, nil
		}
		if msg.Button == tea.MouseButtonLeft {
			if i == m.cursor {
				// Second click on the selected profile connects.
				c := m.cfg.Connections[i]
				return nil, &c
			}
			m.cursor = i
		}
		return nil, nil
	}
	return nil, nil
}

func profileZoneID(i int) string { return fmt.Sprintf("connect:profile:%d", i) }

func (m *connectModel) view() string {
	switch m.mode {
	case connectForm:
		return centerModal(m.width, m.height, m.form.view(min(m.width-10, 90)))
	case connectConfirmDelete:
		name := ""
		if m.cursor < len(m.cfg.Connections) {
			name = m.cfg.Connections[m.cursor].Name
		}
		return centerModal(m.width, m.height,
			fmt.Sprintf("Delete profile %s?\n\n%s", stTitle.Render(name),
				stDim.Render("y confirm · any other key cancel")))
	}

	var b strings.Builder
	b.WriteString(stTitle.Render("MQTT Console") + stDim.Render("  ·  connection profiles") + "\n\n")
	if len(m.cfg.Connections) == 0 {
		b.WriteString(stDim.Render("  no saved profiles yet — press n to create one") + "\n")
	}
	for i, c := range m.cfg.Connections {
		ver := mqttc.Version(c.Version)
		if c.Version == 0 {
			ver = mqttc.V311
		}
		line := fmt.Sprintf("  %-20s %s  %s", c.Name, c.URL, stDim.Render("MQTT "+ver.String()))
		if i == m.cursor {
			line = stSelected.Render(fmt.Sprintf("▸ %-20s %s  MQTT %s", c.Name, c.URL, ver.String()))
		}
		b.WriteString(m.z.Mark(profileZoneID(i), line) + "\n")
	}
	if m.status != "" {
		b.WriteString("\n" + stBad.Render(m.status) + "\n")
	}
	b.WriteString("\n" +
		stDim.Render("enter connect · ") +
		m.z.Mark("connect:new", stDim.Render("n new")) +
		stDim.Render(" · ") +
		m.z.Mark("connect:edit", stDim.Render("e edit")) +
		stDim.Render(" · ") +
		m.z.Mark("connect:delete", stDim.Render("d delete")) +
		stDim.Render(" · q quit"))
	return centerModal(m.width, m.height, b.String())
}
