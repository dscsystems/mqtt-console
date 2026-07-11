package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"
)

// fieldKind selects how a form field is edited and rendered.
type fieldKind int

const (
	fieldText fieldKind = iota
	fieldSecret
	fieldCycle  // pick one of Options with ←/→
	fieldToggle // boolean, toggled with space/←/→
)

type field struct {
	label   string
	kind    fieldKind
	input   textinput.Model
	options []string
	optIdx  int
	on      bool
	hint    string
}

func newTextField(label, value, placeholder, hint string) *field {
	ti := textinput.New()
	ti.SetValue(value)
	ti.Placeholder = placeholder
	ti.Prompt = ""
	ti.CharLimit = 4096
	return &field{label: label, kind: fieldText, input: ti, hint: hint}
}

func newSecretField(label, value, hint string) *field {
	f := newTextField(label, value, "", hint)
	f.kind = fieldSecret
	f.input.EchoMode = textinput.EchoPassword
	f.input.EchoCharacter = '*'
	return f
}

func newCycleField(label string, options []string, selected int, hint string) *field {
	if selected < 0 || selected >= len(options) {
		selected = 0
	}
	return &field{label: label, kind: fieldCycle, options: options, optIdx: selected, hint: hint}
}

func newToggleField(label string, on bool, hint string) *field {
	return &field{label: label, kind: fieldToggle, on: on, hint: hint}
}

// form is a vertical field editor used for connections, publish and subscribe.
type form struct {
	title  string
	fields []*field
	idx    int
	err    string
	z      *zone.Manager
	pre    string // zone id prefix, unique per form instance
}

func newForm(z *zone.Manager, title string, fields ...*field) *form {
	f := &form{title: title, fields: fields, z: z}
	if z != nil {
		f.pre = z.NewPrefix()
	}
	f.focusCurrent()
	return f
}

func (f *form) focusCurrent() {
	for i, fl := range f.fields {
		if fl.kind == fieldText || fl.kind == fieldSecret {
			if i == f.idx {
				fl.input.Focus()
			} else {
				fl.input.Blur()
			}
		}
	}
}

func (f *form) value(i int) string {
	fl := f.fields[i]
	switch fl.kind {
	case fieldCycle:
		return fl.options[fl.optIdx]
	case fieldToggle:
		if fl.on {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(fl.input.Value())
	}
}

func (f *form) boolValue(i int) bool { return f.fields[i].on }

// update processes a key. done reports submit, cancel reports escape.
func (f *form) update(msg tea.Msg) (done, cancel bool, cmd tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return false, false, nil
	}
	cur := f.fields[f.idx]
	switch key.String() {
	case "esc":
		return false, true, nil
	case "enter", "ctrl+s":
		return true, false, nil
	case "tab", "down":
		f.idx = (f.idx + 1) % len(f.fields)
		f.focusCurrent()
		return false, false, nil
	case "shift+tab", "up":
		f.idx = (f.idx - 1 + len(f.fields)) % len(f.fields)
		f.focusCurrent()
		return false, false, nil
	}
	switch cur.kind {
	case fieldCycle:
		switch key.String() {
		case "left":
			cur.optIdx = (cur.optIdx - 1 + len(cur.options)) % len(cur.options)
		case "right", " ":
			cur.optIdx = (cur.optIdx + 1) % len(cur.options)
		}
		return false, false, nil
	case fieldToggle:
		switch key.String() {
		case "left", "right", " ":
			cur.on = !cur.on
		}
		return false, false, nil
	default:
		var c tea.Cmd
		cur.input, c = cur.input.Update(msg)
		return false, false, c
	}
}

// updateMouse handles mouse input: click focuses a field (and cycles or
// toggles choice fields), the wheel moves between fields, and the save and
// cancel labels act as buttons. Mirrors update's return values.
func (f *form) updateMouse(msg tea.MouseMsg) (done, cancel bool) {
	if f.z == nil {
		return false, false
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		f.idx = (f.idx - 1 + len(f.fields)) % len(f.fields)
		f.focusCurrent()
		return false, false
	case tea.MouseButtonWheelDown:
		f.idx = (f.idx + 1) % len(f.fields)
		f.focusCurrent()
		return false, false
	}
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return false, false
	}
	if f.z.Get(f.pre + "submit").InBounds(msg) {
		return true, false
	}
	if f.z.Get(f.pre + "cancel").InBounds(msg) {
		return false, true
	}
	for i, fl := range f.fields {
		if !f.z.Get(f.fieldZoneID(i)).InBounds(msg) {
			continue
		}
		f.idx = i
		f.focusCurrent()
		// Choice fields act immediately, like GUI widgets.
		switch fl.kind {
		case fieldCycle:
			fl.optIdx = (fl.optIdx + 1) % len(fl.options)
		case fieldToggle:
			fl.on = !fl.on
		}
		return false, false
	}
	return false, false
}

func (f *form) fieldZoneID(i int) string { return fmt.Sprintf("%sfield:%d", f.pre, i) }

func (f *form) view(width int) string {
	labelW := 0
	for _, fl := range f.fields {
		if len(fl.label) > labelW {
			labelW = len(fl.label)
		}
	}
	var b strings.Builder
	b.WriteString(stTitle.Render(f.title) + "\n\n")
	for i, fl := range f.fields {
		label := fmt.Sprintf("%*s", labelW, fl.label)
		if i == f.idx {
			label = stKey.Render(label)
		} else {
			label = stDim.Render(label)
		}
		var val string
		switch fl.kind {
		case fieldCycle:
			val = fmt.Sprintf("‹ %s ›", fl.options[fl.optIdx])
			if i == f.idx {
				val = stSelected.Render(val)
			}
		case fieldToggle:
			box := "[ ]"
			if fl.on {
				box = "[x]"
			}
			if i == f.idx {
				box = stSelected.Render(box)
			}
			val = box
		default:
			fl.input.Width = max(20, width-labelW-8)
			val = fl.input.View()
		}
		line := fmt.Sprintf("%s  %s", label, val)
		if f.z != nil {
			line = f.z.Mark(f.fieldZoneID(i), line)
		}
		b.WriteString(line)
		if i == f.idx && fl.hint != "" {
			b.WriteString("\n" + strings.Repeat(" ", labelW+2) + stDim.Render(fl.hint))
		}
		b.WriteString("\n")
	}
	if f.err != "" {
		b.WriteString("\n" + stBad.Render(f.err) + "\n")
	}
	save := stDim.Render("enter save")
	cancel := stDim.Render("esc cancel")
	if f.z != nil {
		save = f.z.Mark(f.pre+"submit", save)
		cancel = f.z.Mark(f.pre+"cancel", cancel)
	}
	b.WriteString("\n" + save + stDim.Render(" · tab/↑↓ move · ←/→ change · ") + cancel)
	return b.String()
}

func centerModal(width, height int, content string) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, stModal.Render(content))
}
