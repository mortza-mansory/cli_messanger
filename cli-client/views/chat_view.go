package views

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"cli-client/models"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// DebugLogFile is set by main so renderMessages can flush before SetText.
// A hard crash inside tview.SetText would otherwise lose the last log lines
// since they're buffered. Flush guarantees the trace is on disk.
var DebugLogFile *os.File

type ChatView struct {
	app           *tview.Application
	container     *tview.Flex
	header        *tview.TextView
	messageView   *tview.TextView
	inputField    *tview.InputField
	footer        *tview.TextView
	commandBar    *tview.TextView
	onSendMessage func(string)
	onCommand     func(string)

	stopped  int32 // atomic: 1 = stopped
	animMode int32 // atomic: 1 = word-by-word, 0 = static

	// Header state — only touched inside tview event loop
	headerUsername string
	headerLatency  int
	headerOnline   bool

	// Server stats — updated by UpdateStats(), only in tview event loop
	statsTotalMsgs  int
	statsActive     int
	statsWaiting    int
	statsMaxMsgs    int
	statsMaxWaiters int
	statsServerURL  string

	// Nick mode / message history — only touched inside tview event loop
	nickActive  bool
	sentHistory []string
	historyIdx  int // -1 = not browsing

	// ── Message render model ──────────────────────────────────────────────
	// All fields below are ONLY ever read/written from inside QueueUpdateDraw
	// (i.e. the tview event loop), so no mutex is needed.
	//
	// Design: the visible text is always:
	//   committedText  +  inFlight[0] + inFlight[1] + ...   (by insertion order)
	//
	// AddMessage      → appends a fully-formatted line to committedText, re-renders.
	// Animation start → allocates an inFlight slot (animID), re-renders.
	// Animation tick  → updates the slot text, re-renders.
	// Animation end   → moves final line from slot into committedText, re-renders.
	//
	// Because AddMessage only touches committedText (never overwrites inFlight),
	// and animations only touch their own slot, messages never clobber each other.
	committedText string
	inFlight      map[int]string // animID → current partial line (with trailing cursor)
	nextAnimID    int            // monotonically increasing; never resets
	inFlightGen   int            // incremented by ClearMessages; stale callbacks bail out
}

func NewChatView(
	app *tview.Application,
	onSendMessage func(string),
	onCommand func(string),
) *ChatView {
	c := &ChatView{
		app:             app,
		onSendMessage:   onSendMessage,
		onCommand:       onCommand,
		historyIdx:      -1,
		headerLatency:   18,
		headerOnline:    true,
		inFlight:        make(map[int]string),
		statsMaxMsgs:    1000,
		statsMaxWaiters: 1000,
		statsServerURL:  "localhost:8034",
	}
	// Default to STATIC mode. Animation mode (word-by-word) involves a
	// goroutine that reads from a channel while holding a QueueUpdateDraw
	// slot — if that path is the crash source, static mode will stay stable.
	// Users can switch with /mode animation once confirmed working.
	atomic.StoreInt32(&c.animMode, 0)
	c.buildUI()
	c.startClockTicker()
	return c
}

func (c *ChatView) Primitive() tview.Primitive      { return c.container }
func (c *ChatView) InputPrimitive() tview.Primitive { return c.inputField }
func (c *ChatView) GetPrimitive() tview.Primitive   { return c.container }

// ── UI construction ────────────────────────────────────────────────────────

func (c *ChatView) buildUI() {
	// Header — bordered box, cyan border to match the project theme.
	// Height 3 in the flex (1 top border + 1 content line + 1 bottom border).
	c.header = tview.NewTextView()
	c.header.SetDynamicColors(true)
	c.header.SetTextAlign(tview.AlignLeft)
	c.header.SetBackgroundColor(tcell.ColorBlack)
	c.header.SetBorder(true)
	c.header.SetBorderColor(tcell.ColorDarkCyan)
	c.header.SetBorderPadding(0, 0, 1, 1)

	c.messageView = tview.NewTextView()
	c.messageView.SetDynamicColors(true)
	c.messageView.SetScrollable(true)
	c.messageView.SetWordWrap(true)
	c.messageView.SetText("")
	c.messageView.SetBackgroundColor(tcell.ColorBlack)

	c.commandBar = tview.NewTextView()
	c.commandBar.SetDynamicColors(true)
	c.commandBar.SetTextAlign(tview.AlignLeft)
	c.commandBar.SetBackgroundColor(tcell.ColorBlack)
	c.redrawCommandBar()

	c.inputField = tview.NewInputField()
	c.inputField.SetLabel("  > ")
	c.inputField.SetPlaceholder("Type a message or /command...")
	c.inputField.SetFieldBackgroundColor(tcell.ColorBlack)
	c.inputField.SetFieldTextColor(tcell.ColorWhite)
	c.inputField.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			text := c.inputField.GetText()
			if text != "" {
				if strings.HasPrefix(text, "/") {
					c.onCommand(text)
				} else {
					c.onSendMessage(text)
				}
				c.inputField.SetText("")
				c.historyIdx = -1
			}
		}
	})

	// ── Arrow-key capture for nick-mode history navigation ─────────────────
	// When nick mode is OFF  → keys behave normally.
	// When nick mode is ON:
	//   ← (Left)  → go to previous (older) sent message.
	//               Only activates when the field is empty OR already in history,
	//               so normal left-cursor movement still works while typing fresh text.
	//   → (Right) → go to next (newer) sent message / clears at the newest end.
	c.inputField.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if !c.nickActive {
			return event
		}
		fieldEmpty := c.inputField.GetText() == ""
		inHistory := c.historyIdx >= 0

		switch event.Key() {
		case tcell.KeyLeft:
			if !fieldEmpty && !inHistory {
				return event // editing a fresh message — let cursor move
			}
			if len(c.sentHistory) == 0 {
				return nil
			}
			if c.historyIdx < 0 {
				c.historyIdx = len(c.sentHistory) - 1
			} else if c.historyIdx > 0 {
				c.historyIdx--
			}
			c.inputField.SetText(c.sentHistory[c.historyIdx])
			return nil // consumed

		case tcell.KeyRight:
			if !fieldEmpty && !inHistory {
				return event // editing a fresh message — let cursor move
			}
			if c.historyIdx < 0 {
				return nil
			}
			c.historyIdx++
			if c.historyIdx >= len(c.sentHistory) {
				c.historyIdx = -1
				c.inputField.SetText("")
			} else {
				c.inputField.SetText(c.sentHistory[c.historyIdx])
			}
			return nil // consumed
		}
		return event
	})

	c.footer = tview.NewTextView()
	c.footer.SetDynamicColors(true)
	c.footer.SetTextAlign(tview.AlignLeft)
	c.footer.SetBackgroundColor(tcell.ColorBlack)
	// initial content drawn after stats fields are set
	c.redrawFooter()

	c.container = tview.NewFlex()
	c.container.SetDirection(tview.FlexRow)
	c.container.SetBackgroundColor(tcell.ColorBlack)
	c.container.AddItem(c.header, 5, 0, false) // 5 = border top + 2 content lines + border bottom
	c.container.AddItem(c.messageView, 0, 1, false)
	c.container.AddItem(c.commandBar, 1, 0, false)
	c.container.AddItem(c.inputField, 3, 0, true)
	c.container.AddItem(c.footer, 1, 0, false)

	c.redrawHeader()
}

// ── Message render engine ──────────────────────────────────────────────────

// sanitizeContent escapes raw user-supplied text for safe rendering inside
// a tview TextView with SetDynamicColors(true).
//
// tview treats anything matching `[word]` as a color/style tag. User messages
// can contain arbitrary `[` characters (URLs, code snippets, IRC nicks like
// `[nick]`). An unmatched or unrecognised `[` sequence causes tview to panic
// with an index-out-of-bounds — a fatal error that recover() cannot catch.
//
// The fix: replace every `[` in user content with `[[]` (tview's own escape
// for a literal `[`). We do NOT escape color tags we intentionally construct
// in format strings — only raw content that came from outside the app.
func sanitizeContent(s string) string {
	return strings.ReplaceAll(s, "[", "[[]")
}

// safeColorTag validates that a color tag from external sources is well-formed
// before inserting it raw into a tview format string.
//
// A valid tview color tag must:
//   - Start with "["
//   - End with "]"
//   - Contain no nested "[" that would start a second tag
//
// Anything that doesn't satisfy these rules is replaced with "[white]" so we
// never hand tview a malformed tag that would cause a fatal index panic.
func safeColorTag(tag string) string {
	if len(tag) < 3 {
		return "[white]"
	}
	if tag[0] != '[' || tag[len(tag)-1] != ']' {
		return "[white]"
	}
	// Must not contain a second "[" inside (would nest tags)
	inner := tag[1 : len(tag)-1]
	if strings.ContainsAny(inner, "[]") {
		return "[white]"
	}
	return tag
}

// renderMessages rebuilds the messageView from the committed buffer plus all
// active in-flight animation lines. Must always be called from the tview event loop.
func (c *ChatView) renderMessages() {
	log.Printf("TRACE renderMessages: committedLen=%d inFlightCount=%d nextAnimID=%d",
		len(c.committedText), len(c.inFlight), c.nextAnimID)
	text := c.committedText
	for i := 0; i < c.nextAnimID; i++ {
		if line, ok := c.inFlight[i]; ok {
			text += line
		}
	}
	log.Printf("TRACE renderMessages: total text len=%d calling SetText", len(text))
	// Flush to disk BEFORE SetText — if tview crashes inside SetText (e.g. from
	// a bad color tag sequence we missed), the log is already on disk.
	if DebugLogFile != nil {
		DebugLogFile.Sync()
	}
	c.messageView.SetText(text)
	log.Printf("TRACE renderMessages: SetText done, calling ScrollToEnd")
	c.messageView.ScrollToEnd()
	log.Printf("TRACE renderMessages: DONE")
}

// ── Message formatting ────────────────────────────────────────────────────

// formatLine renders a Message into a tview-tagged string.
//
// Output format:   [HH:MM] [username] message body
//
// Both the username label (in brackets) and the message content share the
// same color so the entire line visually "belongs" to that user.
// [[] is tview's escape sequence for a literal "[" character.
func formatLine(msg *models.Message) string {
	if msg.IsSystem {
		// System messages are trusted internal strings — they may contain tview
		// color markup like [cyan]name[-] intentionally. Do NOT sanitize them.
		return fmt.Sprintf("[yellow]▸ %s[-]\n", msg.Content)
	}
	color := safeColorTag(msg.Color)
	if color == "" {
		color = "[white]"
	}
	ts := msg.FormatTime()
	safeUser := sanitizeContent(msg.Username) // escapes [ inside username
	safeContent := sanitizeContent(msg.Content)
	// [ts] and [username] are NOT valid tview color names so tview passes them
	// through as literal bracket-wrapped text — no [[] escaping needed.
	// [%s] for timestamp → passes through (digits+colon = never a color name)
	// [[]%s] for username → [[] is tview escape for literal "[", so output is [username]
	return fmt.Sprintf("[gray][%s][-] %s[[]%s][-] %s%s[-]\n",
		ts, color, safeUser, color, safeContent)
}

// incomingPrefix builds the formatted prefix for an incoming message line.
//
// We do NOT escape [ with [[] here. tview passes unrecognised tags (those
// whose content is not a valid color name) through as literal text.
// [10:48] and [username] are never valid tview colors, so they display as-is.
// Real color directives like [red] and [-] work as normal.
func incomingPrefix(colorTag, username string) string {
	ts := time.Now().Format("15:04")
	safeUser := sanitizeContent(username) // escapes any [ inside the username itself
	return fmt.Sprintf("[gray][%s][-] %s[[]%s][-] %s",
		ts, colorTag, safeUser, colorTag)
}

// ── Public message API ────────────────────────────────────────────────────

// AddMessage displays a message instantly (own messages, system messages).
// Must be called from the tview event loop.
//
// By appending to committedText (never to the raw messageView text), we
// guarantee the message survives any concurrent animation redraws.
func (c *ChatView) AddMessage(msg *models.Message) {
	c.committedText += formatLine(msg)
	c.renderMessages()
}

// AddIncomingMessage displays a message from another user.
//
//	colorTag — tview color tag from the wire format, e.g. "[green]" or "[#ff00ff]".
//	           Pass through models.ParseColorToTag if converting from raw JSON.
//
// Static mode  → appends to committedText immediately, one draw call.
// Anim mode    → allocates an in-flight slot, drips words via a goroutine.
//
// In both modes, any messages sent by the local user while this call is in
// progress are appended to committedText and will NOT be lost.
//
// Safe to call from any goroutine.
func (c *ChatView) AddIncomingMessage(username, content, colorTag string) {
	log.Printf("TRACE AddIncomingMessage: ENTER user=%q color=%q content=%.80q", username, colorTag, content)

	if atomic.LoadInt32(&c.stopped) == 1 {
		log.Printf("TRACE AddIncomingMessage: view stopped, dropping msg from %q", username)
		return
	}

	// Normalise and validate color tag.
	// safeColorTag MUST run last — it rejects any tag that would crash tview.
	if colorTag == "" {
		colorTag = models.GetUsernameColor(username)
	}
	if !strings.HasPrefix(colorTag, "[") {
		colorTag = models.ParseColorToTag(colorTag)
	}
	colorTag = safeColorTag(colorTag) // reject malformed tags from the server
	log.Printf("TRACE AddIncomingMessage: normalised+validated colorTag=%q", colorTag)

	words := strings.Fields(content)
	log.Printf("TRACE AddIncomingMessage: word count=%d", len(words))
	if len(words) == 0 {
		return
	}

	prefix := incomingPrefix(colorTag, username)
	log.Printf("TRACE AddIncomingMessage: prefix built, animMode=%d", atomic.LoadInt32(&c.animMode))

	// ── STATIC mode ────────────────────────────────────────────────────────
	if atomic.LoadInt32(&c.animMode) == 0 {
		log.Printf("TRACE AddIncomingMessage: static mode, queuing draw for user=%q", username)
		c.app.QueueUpdateDraw(func() {
			log.Printf("TRACE static draw: ENTER event loop for user=%q", username)
			if atomic.LoadInt32(&c.stopped) == 1 {
				log.Printf("TRACE static draw: stopped, bailing")
				return
			}
			defer func() {
				if r := recover(); r != nil {
					log.Printf("PANIC static draw (from %s): %v", username, r)
				}
			}()
			sanitized := sanitizeContent(content)
			log.Printf("TRACE static draw: sanitized content=%.80q", sanitized)
			log.Printf("TRACE static draw: committedText len before=%d", len(c.committedText))
			c.committedText += prefix + sanitized + "[-]\n" // prefix already ends with colorTag
			log.Printf("TRACE static draw: committedText len after=%d inFlight count=%d", len(c.committedText), len(c.inFlight))
			log.Printf("TRACE static draw: calling renderMessages")
			c.renderMessages()
			log.Printf("TRACE static draw: renderMessages returned")
		})
		log.Printf("TRACE AddIncomingMessage: static QueueUpdateDraw enqueued")
		return
	}

	// ── ANIMATION mode ─────────────────────────────────────────────────────
	// Step 1 (event loop): allocate an in-flight slot and paint the cursor
	// immediately so the user sees activity straight away.
	// idCh carries both the animID and the inFlightGen at allocation time.
	// The animation goroutine uses gen to detect if ClearMessages() ran while
	// it was mid-flight, so it can discard stale word-tick callbacks.
	log.Printf("TRACE AddIncomingMessage: anim mode, allocating slot for user=%q", username)
	type animSlot struct{ id, gen int }
	slotCh := make(chan animSlot, 1)
	c.app.QueueUpdateDraw(func() {
		log.Printf("TRACE anim-init: ENTER event loop for user=%q", username)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC anim-init (from %s): %v", username, r)
				slotCh <- animSlot{-1, -1}
			}
		}()
		if atomic.LoadInt32(&c.stopped) == 1 {
			log.Printf("TRACE anim-init: stopped, sending -1 slot")
			slotCh <- animSlot{-1, -1}
			return
		}
		animID := c.nextAnimID
		c.nextAnimID++
		gen := c.inFlightGen
		log.Printf("TRACE anim-init: allocated animID=%d gen=%d inFlight count=%d", animID, gen, len(c.inFlight))
		c.inFlight[animID] = prefix + "[dim]▋[-]"
		slotCh <- animSlot{animID, gen}
		log.Printf("TRACE anim-init: calling renderMessages")
		c.renderMessages()
		log.Printf("TRACE anim-init: renderMessages returned, sent slot")
	})
	log.Printf("TRACE AddIncomingMessage: anim init QueueUpdateDraw enqueued")

	// Step 2 (goroutine): drip words one at a time, updating only our slot.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC word-anim goroutine (from %s): %v", username, r)
			}
		}()

		log.Printf("TRACE anim-goroutine: waiting for slot user=%q", username)
		slot := <-slotCh
		log.Printf("TRACE anim-goroutine: got slot id=%d gen=%d user=%q", slot.id, slot.gen, username)
		if slot.id < 0 || atomic.LoadInt32(&c.stopped) == 1 {
			log.Printf("TRACE anim-goroutine: aborting (id=%d stopped=%d)", slot.id, atomic.LoadInt32(&c.stopped))
			return
		}
		animID := slot.id
		myGen := slot.gen

		built := ""
		for i, word := range words {
			if atomic.LoadInt32(&c.stopped) == 1 {
				return
			}

			// Variable delay: natural rhythm — short words fast, long ones slightly slower.
			delay := time.Duration(55+len(word)*9) * time.Millisecond
			if delay > 150*time.Millisecond {
				delay = 150 * time.Millisecond
			}
			time.Sleep(delay)

			if i == 0 {
				built = word
			} else {
				built += " " + word
			}
			isLast := i == len(words)-1
			snapshot := built

			wordIdx := i
			c.app.QueueUpdateDraw(func() {
				log.Printf("TRACE word-tick: ENTER event loop animID=%d word[%d]=%q isLast=%v user=%q", animID, wordIdx, snapshot, isLast, username)
				defer func() {
					if r := recover(); r != nil {
						log.Printf("PANIC word-anim draw (from %s): %v", username, r)
					}
				}()
				if atomic.LoadInt32(&c.stopped) == 1 {
					log.Printf("TRACE word-tick: stopped, bailing animID=%d", animID)
					return
				}
				if c.inFlightGen != myGen {
					log.Printf("TRACE word-tick: stale gen (mine=%d current=%d), bailing animID=%d", myGen, c.inFlightGen, animID)
					return
				}
				sanitized := sanitizeContent(snapshot)
				log.Printf("TRACE word-tick: sanitized=%.60q committedLen=%d inFlightCount=%d", sanitized, len(c.committedText), len(c.inFlight))
				if isLast {
					log.Printf("TRACE word-tick: LAST WORD — committing animID=%d", animID)
					delete(c.inFlight, animID)
					c.committedText += prefix + sanitized + "[-]\n"
					log.Printf("TRACE word-tick: committed, new committedLen=%d", len(c.committedText))
				} else {
					c.inFlight[animID] = prefix + sanitized + " [dim]▋[-]"
				}
				log.Printf("TRACE word-tick: calling renderMessages animID=%d", animID)
				c.renderMessages()
				log.Printf("TRACE word-tick: renderMessages returned animID=%d", animID)
			})
		}
	}()
}

// SetMessages bulk-loads a slice of messages without animation.
// Replaces committedText entirely and clears any in-flight animations.
func (c *ChatView) SetMessages(messages []*models.Message) {
	if atomic.LoadInt32(&c.stopped) == 1 {
		return
	}
	c.app.QueueUpdateDraw(func() {
		if atomic.LoadInt32(&c.stopped) == 1 {
			return
		}
		var b strings.Builder
		for _, msg := range messages {
			b.WriteString(formatLine(msg))
		}
		c.committedText = b.String()
		c.inFlight = make(map[int]string) // discard any in-flight animations
		c.renderMessages()
	})
}

// ClearMessages wipes the message area and all in-flight animation state.
// Must be called from the tview event loop.
//
// Bumping inFlightGen invalidates any word-tick callbacks that were already
// queued when this runs — they check the generation and bail out rather than
// writing to a map that has been replaced.
func (c *ChatView) ClearMessages() {
	c.committedText = ""
	c.inFlight = make(map[int]string)
	c.inFlightGen++ // invalidate all queued animation callbacks
	c.renderMessages()
}

// ── Header ─────────────────────────────────────────────────────────────────

func (c *ChatView) startClockTicker() {
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if atomic.LoadInt32(&c.stopped) == 1 {
				return
			}
			c.app.QueueUpdateDraw(func() {
				if atomic.LoadInt32(&c.stopped) == 1 {
					return
				}
				c.redrawHeader()
			})
		}
	}()
}

// redrawHeader repaints the header content.
//
// Row 1:  [GLOBAL]  HH:MM:SS  @username    ●ONLINE/OFFLINE  LATENCY:Xms
// Row 2:  msgs ▓▓▓▓▓░░░░░ 47/1000  │  ●●●○○ 3 active  │  0 waiting
//
// Must be called from within the tview event loop.
func (c *ChatView) redrawHeader() {
	clock := time.Now().Format("15:04:05")

	// ── Row 1 ────────────────────────────────────────────────────────────────
	onlineStr := "[red]● OFFLINE[-]"
	if c.headerOnline {
		onlineStr = "[green]● ONLINE[-]"
	}

	userStr := ""
	if c.headerUsername != "" {
		userStr = fmt.Sprintf("  [yellow]@%s[-]", c.headerUsername)
	}

	latencyColor := "green"
	if c.headerLatency > 100 {
		latencyColor = "yellow"
	}
	if c.headerLatency > 300 {
		latencyColor = "red"
	}
	latencyStr := "[dim]ping: --ms[-]"
	if c.headerLatency >= 0 {
		latencyStr = fmt.Sprintf("[dim]ping: [%s]%dms[-][-]", latencyColor, c.headerLatency)
	}

	row1 := fmt.Sprintf("[cyan]◈ GLOBAL[-]  [dim]%s[-]%s    %s   %s",
		clock, userStr, onlineStr, latencyStr)

	// ── Row 2: live server stats ─────────────────────────────────────────────
	// Active users: up to 5 colored dots, then "+N"
	activeDots := ""
	dotColors := []string{"green", "cyan", "yellow", "magenta", "blue"}
	n := c.statsActive
	if n > 5 {
		for i := 0; i < 5; i++ {
			activeDots += fmt.Sprintf("[%s]●[-]", dotColors[i])
		}
		activeDots += fmt.Sprintf("[dim]+%d[-]", n-5)
	} else {
		for i := 0; i < 5; i++ {
			if i < n {
				activeDots += fmt.Sprintf("[%s]●[-]", dotColors[i])
			} else {
				activeDots += "[dim]○[-]"
			}
		}
	}

	waitColor := "dim"
	if c.statsWaiting > 0 {
		waitColor = "cyan"
	}

	row2 := fmt.Sprintf(
		"[dim]total msgs: [-][cyan]%d[-]   [dim]│[-]   %s [dim]%d active[-]   [dim]│   [%s]%d waiting[-][-]",
		c.statsTotalMsgs,
		activeDots, c.statsActive,
		waitColor, c.statsWaiting,
	)

	c.header.SetText(row1 + "\n" + row2)
}

// UpdateStats refreshes the server stats displayed in the header and footer.
// Safe to call from any goroutine.
func (c *ChatView) UpdateStats(totalMsgs, active, waiting, maxMsgs, maxWaiters int, serverURL string) {
	if atomic.LoadInt32(&c.stopped) == 1 {
		return
	}
	c.app.QueueUpdateDraw(func() {
		if atomic.LoadInt32(&c.stopped) == 1 {
			return
		}
		c.statsTotalMsgs = totalMsgs
		c.statsActive = active
		c.statsWaiting = waiting
		c.statsMaxMsgs = maxMsgs
		c.statsMaxWaiters = maxWaiters
		if serverURL != "" {
			c.statsServerURL = serverURL
		}
		c.redrawHeader()
		c.redrawFooter()
	})
}

// SetCurrentUser pushes the logged-in username to the header.
// Must be called from the tview event loop.
func (c *ChatView) SetCurrentUser(username string) {
	c.headerUsername = username
	c.redrawHeader()
}

// SetOnlineStatus updates the ●ONLINE/●OFFLINE indicator in the header.
//
// MUST be called from within the tview event loop (i.e. from inside a
// QueueUpdateDraw callback). It does NOT call QueueUpdateDraw itself —
// doing so from inside an existing callback would nest queue calls and
// deadlock tview's updates channel on Windows.
func (c *ChatView) SetOnlineStatus(online bool) {
	if atomic.LoadInt32(&c.stopped) == 1 {
		return
	}
	c.headerOnline = online
	c.redrawHeader()
}

// SetOnlineStatusAsync updates the online indicator from any goroutine.
// Use this ONLY when NOT already inside a QueueUpdateDraw callback.
func (c *ChatView) SetOnlineStatusAsync(online bool) {
	if atomic.LoadInt32(&c.stopped) == 1 {
		return
	}
	c.app.QueueUpdateDraw(func() {
		if atomic.LoadInt32(&c.stopped) == 1 {
			return
		}
		c.headerOnline = online
		c.redrawHeader()
	})
}

// UpdateLatency updates the latency shown in the header.
// Safe to call from any goroutine.
func (c *ChatView) UpdateLatency(latency int) {
	if atomic.LoadInt32(&c.stopped) == 1 {
		return
	}
	c.app.QueueUpdateDraw(func() {
		if atomic.LoadInt32(&c.stopped) == 1 {
			return
		}
		c.headerLatency = latency
		c.redrawHeader()
	})
}

// ── Command bar ───────────────────────────────────────────────────────────

func (c *ChatView) redrawCommandBar() {
	modeLabel := "[dim]mode:[green]ANIM[-]"
	if atomic.LoadInt32(&c.animMode) == 0 {
		modeLabel = "[dim]mode:[cyan]STATIC[-]"
	}
	nickLabel := ""
	if c.nickActive {
		nickLabel = "  [cyan]nick:ON ←→[-]"
	}
	c.commandBar.SetText(fmt.Sprintf(
		"[dim]/ commands: clear  whois  nick  mode  user_color  latency  info  exit  help[-]   %s%s",
		modeLabel, nickLabel,
	))
	c.redrawFooter() // keep mode label in footer in sync
}

// redrawFooter repaints the bottom status bar with secondary server info.
// Must be called from within the tview event loop.
func (c *ChatView) redrawFooter() {
	if c.footer == nil {
		return // called before buildUI() finished initializing c.footer
	}

	modeLabel := "[cyan]ANIM[-]"
	if atomic.LoadInt32(&c.animMode) == 0 {
		modeLabel = "[green]STATIC[-]"
	}

	url := c.statsServerURL
	if url == "" {
		url = "localhost:8034"
	}

	c.footer.SetText(fmt.Sprintf(
		"[dim]server:[cyan]%s[-]  [dim]│  mode:%s[-]  [dim]│[-]  [magenta]SecTherminal v1.0[-]",
		url, modeLabel,
	))
}

// ── Animation mode ────────────────────────────────────────────────────────

func (c *ChatView) SetAnimationMode(anim bool) {
	if anim {
		atomic.StoreInt32(&c.animMode, 1)
	} else {
		atomic.StoreInt32(&c.animMode, 0)
	}
	c.redrawCommandBar()
}

func (c *ChatView) ToggleAnimationMode() string {
	if atomic.LoadInt32(&c.animMode) == 1 {
		atomic.StoreInt32(&c.animMode, 0)
		c.redrawCommandBar()
		return "static"
	}
	atomic.StoreInt32(&c.animMode, 1)
	c.redrawCommandBar()
	return "animation"
}

func (c *ChatView) IsAnimationMode() bool {
	return atomic.LoadInt32(&c.animMode) == 1
}

// ── Nick mode ─────────────────────────────────────────────────────────────

func (c *ChatView) ToggleNickMode() bool {
	c.nickActive = !c.nickActive
	c.historyIdx = -1
	c.redrawCommandBar()
	return c.nickActive
}

func (c *ChatView) AddToHistory(msg string) {
	if msg == "" {
		return
	}
	if len(c.sentHistory) > 0 && c.sentHistory[len(c.sentHistory)-1] == msg {
		return
	}
	c.sentHistory = append(c.sentHistory, msg)
	if len(c.sentHistory) > 100 {
		c.sentHistory = c.sentHistory[1:]
	}
}

// ── Footer ────────────────────────────────────────────────────────────────

func (c *ChatView) UpdateCursorPosition(line, col int) {
	if atomic.LoadInt32(&c.stopped) == 1 {
		return
	}
	c.app.QueueUpdateDraw(func() {
		if atomic.LoadInt32(&c.stopped) == 1 {
			return
		}
		c.footer.SetText(fmt.Sprintf(
			"[magenta]NORMAL[-]    SecTherminal              UTF-8    L:%d, C:%d", line, col,
		))
	})
}

// Stop signals this view is permanently done. No further UI updates will run.
func (c *ChatView) Stop() {
	atomic.StoreInt32(&c.stopped, 1)
}
