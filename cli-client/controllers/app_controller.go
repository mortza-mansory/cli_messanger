package controllers

import (
	"fmt"
	"strings"
	"time"

	"cli-client/models"
	"cli-client/views"

	"github.com/rivo/tview"
)

type AppController struct {
	App   *models.AppState
	Views map[models.Screen]interface{}
	SM    *StateMachine

	app         *tview.Application
	netClient   *NetworkClient
	latencyCtrl *LatencyController
}

func NewAppController(app *tview.Application) *AppController {
	return &AppController{
		App:   models.NewAppState(),
		Views: make(map[models.Screen]interface{}),
		SM:    NewStateMachine(models.ScreenNone),
		app:   app,
	}
}

func (ac *AppController) RegisterView(screen models.Screen, view interface{}) {
	ac.Views[screen] = view
}

// OnLoginSubmit — called from the tview event loop.
// username is the entered username; colorTag is the tview color tag chosen
// during login (e.g. "[cyan]"). If empty, falls back to hash-based default.
func (ac *AppController) OnLoginSubmit(username, colorTag string) {
	ac.App.SetCurrentUser(username)

	// Apply the color chosen during login immediately, before any messages render.
	if colorTag != "" && strings.HasPrefix(colorTag, "[") {
		ac.App.SetUserColor(username, colorTag)
	}

	ac.SM.Transition(models.ScreenChat)

	if chat, ok := ac.Views[models.ScreenChat].(*views.ChatView); ok {
		chat.SetCurrentUser(username)
	}

	ac.startNetworkClient()
	ac.startLatencyController()
}

// OnSendMessage — called from the tview event loop.
// The message is displayed optimistically in the UI immediately.
// The encrypted wire copy is sent to the server asynchronously.
func (ac *AppController) OnSendMessage(content string) {
	msg := models.NewMessage(ac.App.CurrentUser.Username, content)
	msg.Color = ac.App.GetUserColorTag(ac.App.CurrentUser.Username)
	ac.App.AddMessage(msg)

	// Display immediately — no waiting for server round-trip.
	if chat, ok := ac.Views[models.ScreenChat].(*views.ChatView); ok {
		chat.AddMessage(msg)
		chat.AddToHistory(content)
	}

	// Fire-and-forget: encrypt and relay to server.
	// The server echoes this back to us; NetworkClient deduplicates via sentIDs.
	if ac.netClient != nil {
		ac.netClient.SendMessage(msg.Username, content, msg.Color)
	}
}

// OnCommand — called from the tview event loop.
func (ac *AppController) OnCommand(command string) {
	if len(command) <= 1 {
		ac.sendSystem("Usage: /<command>  —  type /help for available commands.")
		return
	}

	raw := command[1:]
	parts := strings.SplitN(raw, " ", 2)
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}

	chat, hasChat := ac.Views[models.ScreenChat].(*views.ChatView)

	switch cmd {

	case "clear":
		ac.App.Messages = []*models.Message{}
		if hasChat {
			chat.ClearMessages()
		}

	case "help":
		ac.sendSystem("Commands:  /clear  /whois  /nick  /mode [animation|static]  /user_color <color>  /server <url>  /latency  /info  /exit  /help")

	case "info":
		lines := []string{
			"[dim]┌─ SecTherminal ──────────────────────────────────────────────┐[-]",
			"  A lightweight, encrypted terminal messenger built in Go.",
			"  Designed for speed, privacy, and minimal footprint.",
			"",
			"  [cyan]Author   [-]Mortza Mansory",
			"  [cyan]License  [-]MIT — free and open-source",
			"  [cyan]GitHub   [-]https://github.com/mortza-mansory/TTC-cli-messanger",
			"  [cyan]Version  [-]v1.0.0-dev",
			"",
			"  [green]✓[-] End-to-end AES-256-GCM encrypted relay",
			"  [green]✓[-] Zero server-side message storage — your device, your data",
			"  [green]✓[-] Client-side history only (server stores nothing)",
			"  [green]✓[-] Open source — audit the code yourself",
			"  [green]✓[-] Low latency global relay nodes",
			"[dim]└─────────────────────────────────────────────────────────────┘[-]",
		}
		for _, line := range lines {
			ac.sendSystem(line)
		}

	case "whois":
		if ac.App.CurrentUser == nil {
			ac.sendSystem("No user logged in.")
			return
		}
		u := ac.App.CurrentUser
		colorTag := ac.App.GetUserColorTag(u.Username)
		colorDisplay := strings.Trim(colorTag, "[]")
		ac.sendSystem(fmt.Sprintf(
			"Whois  ▸  user: %s%s[-]  |  color: %s  |  status: online  |  msgs sent: %d",
			colorTag, u.Username, colorDisplay, ac.countUserMessages(u.Username),
		))

	case "nick":
		if !hasChat {
			return
		}
		active := chat.ToggleNickMode()
		if active {
			ac.sendSystem("Nick mode ON — ← / → navigates your sent-message history. /nick to turn off.")
		} else {
			ac.sendSystem("Nick mode OFF — arrow keys restored to normal.")
		}

	case "mode":
		if !hasChat {
			return
		}
		var label string
		switch strings.ToLower(arg) {
		case "animation", "anim":
			chat.SetAnimationMode(true)
			label = "animation"
		case "static":
			chat.SetAnimationMode(false)
			label = "static"
		default:
			label = chat.ToggleAnimationMode()
		}
		ac.sendSystem(fmt.Sprintf("Display mode → %s", label))

	case "user_color":
		if ac.App.CurrentUser == nil {
			ac.sendSystem("No user logged in.")
			return
		}
		if arg == "" {
			validList := strings.Join(models.ValidNamedColors, ", ")
			ac.sendSystem("Usage: /user_color <color>  —  named: " + validList + "  |  or hex: #rrggbb")
			return
		}
		username := ac.App.CurrentUser.Username
		if strings.ToLower(arg) == "reset" {
			delete(ac.App.UserColors, username)
			defaultTag := models.GetUsernameColor(username)
			if hasChat {
				chat.SetCurrentUser(username)
			}
			colorDisplay := strings.Trim(defaultTag, "[]")
			ac.sendSystem(fmt.Sprintf("Color reset → %s%s[-] (default)", defaultTag, colorDisplay))
			return
		}
		colorTag := models.ParseColorToTag(arg)
		if !strings.HasPrefix(arg, "#") && !models.IsValidNamedColor(arg) {
			validList := strings.Join(models.ValidNamedColors, ", ")
			ac.sendSystem(fmt.Sprintf("Unknown color: '%s'  —  valid names: %s  |  or hex: #rrggbb", arg, validList))
			return
		}
		ac.App.SetUserColor(username, colorTag)
		colorDisplay := arg
		if !strings.HasPrefix(arg, "#") {
			colorDisplay = strings.Trim(colorTag, "[]")
		}
		ac.sendSystem(fmt.Sprintf("Your color → %s%s[-]  (applies to all your new messages)", colorTag, colorDisplay))

	// ── /server ──────────────────────────────────────────────────────────────
	// Changes the relay server URL at runtime and reconnects.
	// Usage: /server http://myserver.example.com:8080
	case "server":
		if arg == "" {
			current := DefaultServerURL
			if ac.netClient != nil {
				current = ac.netClient.serverURL
			}
			ac.sendSystem(fmt.Sprintf("Current server: [cyan]%s[-]  —  usage: /server <url>", current))
			return
		}
		// Validate basic URL shape
		if !strings.HasPrefix(arg, "http://") && !strings.HasPrefix(arg, "https://") {
			ac.sendSystem("Invalid URL — must start with http:// or https://")
			return
		}
		DefaultServerURL = arg
		ac.sendSystem(fmt.Sprintf("Server URL → [cyan]%s[-]  — reconnecting…", arg))
		// Restart the network client with the new URL
		ac.stopNetworkClient()
		ac.startNetworkClient()

	case "latency":
		ms := -1
		if ac.latencyCtrl != nil {
			ms = ac.latencyCtrl.Current()
		}
		if ms < 0 {
			ac.sendSystem("Latency: unreachable — TCP probe to 1.1.1.1:53 failed.")
		} else {
			ac.sendSystem(fmt.Sprintf("Latency: [cyan]%dms[-]  (TCP probe → 1.1.1.1:53, live measurement)", ms))
		}

	case "exit":
		ac.app.Stop()

	default:
		ac.sendSystem(fmt.Sprintf("Unknown command: /%s — type /help for available commands.", cmd))
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (ac *AppController) sendSystem(text string) {
	msg := models.NewSystemMessage(text)
	ac.App.AddMessage(msg)
	if chat, ok := ac.Views[models.ScreenChat].(*views.ChatView); ok {
		chat.AddMessage(msg)
	}
}

func (ac *AppController) countUserMessages(username string) int {
	n := 0
	for _, m := range ac.App.Messages {
		if m.Username == username {
			n++
		}
	}
	return n
}

// startNetworkClient creates and starts a NetworkClient using DefaultServerURL.
func (ac *AppController) startNetworkClient() {
	ac.stopNetworkClient()

	ac.netClient = NewNetworkClient(
		ac.app,
		DefaultServerURL,

		// onMessage: called from the poll goroutine for each decrypted incoming message.
		func(username, content, colorTag string) {
			if chat, ok := ac.Views[models.ScreenChat].(*views.ChatView); ok {
				// AddIncomingMessage already wraps in QueueUpdateDraw — safe here.
				chat.AddIncomingMessage(username, content, colorTag)
			}
		},

		// onStatusChange: called from the poll goroutine on connect/error/reconnect.
		func(connected bool, msg string) {
			ac.app.QueueUpdateDraw(func() {
				ac.App.IsConnected = connected
				ac.sendSystem(msg)
				if chat, ok := ac.Views[models.ScreenChat].(*views.ChatView); ok {
					chat.SetOnlineStatus(connected)
				}
			})
		},
	)

	ac.netClient.Start()
	go ac.statsPollerLoop()
}

func (ac *AppController) statsPollerLoop() {
	// Poll /api/stats every 8 seconds and push results to the chat header.
	// Runs as a goroutine alongside the poll loop; stops when netClient stops.
	ticker := time.NewTicker(8 * time.Second)
	defer ticker.Stop()

	// Fetch once immediately so header shows data before the first tick.
	ac.fetchAndPushStats()

	for {
		select {
		case <-ticker.C:
			if ac.netClient == nil {
				return
			}
			ac.fetchAndPushStats()
		}
	}
}

func (ac *AppController) fetchAndPushStats() {
	if ac.netClient == nil {
		return
	}
	stats, err := ac.netClient.FetchStats()
	if err != nil {
		return // non-critical — silently skip bad fetches
	}
	chat, ok := ac.Views[models.ScreenChat].(*views.ChatView)
	if !ok {
		return
	}
	chat.UpdateStats(
		stats.ChatStats.TotalMessages,
		stats.ActiveClients,
		stats.ChatStats.WaitingClients,
		stats.ChatStats.MaxWaiters, // reuse maxWaiters as maxMsgs (server exposes 1000 for both)
		stats.ChatStats.MaxWaiters,
		ac.netClient.ServerURL(),
	)
}

func (ac *AppController) stopNetworkClient() {
	if ac.netClient != nil {
		ac.netClient.Stop()
		ac.netClient = nil
	}
}

func (ac *AppController) startLatencyController() {
	if ac.latencyCtrl != nil {
		ac.latencyCtrl.Stop()
	}
	ac.latencyCtrl = NewLatencyController()
	ac.latencyCtrl.Start(func(ms int) {
		ac.App.Latency = ms
		if chat, ok := ac.Views[models.ScreenChat].(*views.ChatView); ok {
			chat.UpdateLatency(ms)
		}
	})
}

// StopBot stops all background services: network client and latency controller.
func (ac *AppController) StopBot() {
	ac.stopNetworkClient()
	if ac.latencyCtrl != nil {
		ac.latencyCtrl.Stop()
		ac.latencyCtrl = nil
	}
}
