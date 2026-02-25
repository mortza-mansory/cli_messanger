package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// colorOption represents one selectable color in the login palette.
type colorOption struct {
	name    string // plain name sent to the server e.g. "cyan"
	tag     string // tview tag e.g. "[cyan]"
	display string // label shown in the picker e.g. "Cyan"
}

var loginColors = []colorOption{
	{"red", "[red]", "Red"},
	{"green", "[green]", "Green"},
	{"yellow", "[yellow]", "Yellow"},
	{"cyan", "[cyan]", "Cyan"},
	{"magenta", "[magenta]", "Magenta"},
	{"blue", "[blue]", "Blue"},
	{"white", "[white]", "White"},
}

// LoginView steps:
//
//	0 — enter username
//	1 — pick color from palette
//	2 — enter password (optional / ignored by backend)
type LoginView struct {
	app         *tview.Application
	container   *tview.Flex
	headerBox   *tview.Box
	textView    *tview.TextView
	inputField  *tview.InputField
	onSubmit    func(username, color string)
	currentStep int
	username    string
	chosenColor string // tview tag e.g. "[cyan]"
}

func NewLoginView(
	app *tview.Application,
	onSubmit func(string, string),
) *LoginView {
	l := &LoginView{
		app:         app,
		onSubmit:    onSubmit,
		currentStep: 0,
		chosenColor: "[cyan]", // sensible default
	}
	l.buildUI()
	return l
}

func (l *LoginView) Primitive() tview.Primitive    { return l.container }
func (l *LoginView) GetPrimitive() tview.Primitive { return l.container }

func (l *LoginView) buildUI() {
	l.headerBox = tview.NewBox()
	l.headerBox.SetBorder(true)
	l.headerBox.SetTitle(" TERMINAL MESSENGER v1.0.0 ")
	l.headerBox.SetBackgroundColor(tcell.ColorBlack)

	l.textView = tview.NewTextView()
	l.textView.SetDynamicColors(true)
	l.textView.SetTextAlign(tview.AlignLeft)
	l.textView.SetBackgroundColor(tcell.ColorBlack)

	l.inputField = tview.NewInputField()
	l.inputField.SetLabel("> ")
	l.inputField.SetPlaceholder("Type here...")
	l.inputField.SetFieldBackgroundColor(tcell.ColorBlack)
	l.inputField.SetFieldTextColor(tcell.ColorWhite)
	l.inputField.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			l.handleEnter()
		}
	})

	l.container = tview.NewFlex()
	l.container.SetDirection(tview.FlexRow)
	l.container.SetBackgroundColor(tcell.ColorBlack)
	l.container.AddItem(l.headerBox, 3, 0, false)
	l.container.AddItem(l.textView, 0, 1, false)
	l.container.AddItem(l.inputField, 1, 0, true)
}

func (l *LoginView) handleEnter() {
	text := strings.TrimSpace(l.inputField.GetText())
	l.inputField.SetText("")

	switch l.currentStep {

	// ── Step 0: username ─────────────────────────────────────────────────────
	case 0:
		if text == "" {
			return
		}
		l.username = text
		l.currentStep = 1
		l.showColorPicker()

	// ── Step 1: color pick ───────────────────────────────────────────────────
	case 1:
		// Accept a number 1-N or a color name
		chosen := l.parseColorInput(text)
		if chosen == nil {
			l.typewriterText(fmt.Sprintf(
				"\n[red]Unknown choice '%s'. Enter a number (1-%d) or a color name.[white]\n",
				text, len(loginColors),
			))
			return
		}
		l.chosenColor = chosen.tag
		l.currentStep = 2

		preview := fmt.Sprintf("\n%s● %s[-]  [dim]— your messages will appear in this color[-]\n", chosen.tag, chosen.display)
		l.typewriterText(preview)
		time.Sleep(50 * time.Millisecond)
		l.typewriterText("\n[cyan]Enter a password[dim] (or press Enter to skip):[white] ")

	// ── Step 2: password (optional) ──────────────────────────────────────────
	case 2:
		// Password is cosmetic — backend ignores it.
		// Pass chosenColor as the second argument so the controller can apply it.
		l.onSubmit(l.username, l.chosenColor)
	}
}

// showColorPicker appends the color palette to the textView.
func (l *LoginView) showColorPicker() {
	var sb strings.Builder
	sb.WriteString("\n[cyan]Choose your chat color:[white]\n\n")
	for i, c := range loginColors {
		sb.WriteString(fmt.Sprintf(
			"  [dim]%d.[white]  %s██[-]  %s%s[-]\n",
			i+1, c.tag, c.tag, c.display,
		))
	}
	sb.WriteString("\n[dim]Type a number (1-7) or a color name:[white] ")
	l.typewriterText(sb.String())
}

// parseColorInput accepts "1"–"7" or a plain name like "cyan".
func (l *LoginView) parseColorInput(input string) *colorOption {
	input = strings.ToLower(strings.TrimSpace(input))
	// Numeric shortcut
	for i, c := range loginColors {
		if input == fmt.Sprintf("%d", i+1) {
			return &loginColors[i]
		}
		if input == c.name {
			return &loginColors[i]
		}
	}
	return nil
}

// typewriterText displays text character by character for the terminal feel.
func (l *LoginView) typewriterText(text string) {
	go func() {
		for _, char := range text {
			l.app.QueueUpdateDraw(func() {
				current := l.textView.GetText(false)
				l.textView.SetText(current + string(char))
			})
			time.Sleep(10 * time.Millisecond)
		}
	}()
}

func (l *LoginView) StartUsernamePrompt() {
	l.currentStep = 0
	l.typewriterText(`[yellow]! Establishing secure connection...[white]
[green]✓ Connection established.[white]

[cyan]Tell us your username:[white] `)
}
