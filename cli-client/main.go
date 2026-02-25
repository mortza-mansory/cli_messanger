package main

import (
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"time"

	"cli-client/controllers"
	"cli-client/models"
	"cli-client/views"

	"github.com/rivo/tview"
)

var logFile *os.File

func init() {
	var err error
	// Open with append+create so multiple runs accumulate — easier to correlate
	// a crash with the session that produced it.
	logFile, err = os.OpenFile("error.txt",
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Println("Failed to open error log file:", err)
		return
	}

	// Also wire up the standard logger to the same file.
	log.SetOutput(&syncWriter{f: logFile})

	// Give chat_view.go access to the file handle so it can flush before
	// every tview SetText call — ensures traces are on disk even on hard crashes.
	views.DebugLogFile = logFile
	// syncWriter flushes to disk on every write — no log line lost on hard crash
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)

	// Write a session header so different runs are clearly separated in the file.
	separator := fmt.Sprintf(
		"\n════════════════════════════════════════════════════════\n"+
			"  TTC session started  %s\n"+
			"════════════════════════════════════════════════════════\n",
		time.Now().Format("2006-01-02 15:04:05"),
	)
	logFile.WriteString(separator)
	logFile.Sync()
}

// logError writes a timestamped error line to error.txt and stderr.
// syncWriter wraps an *os.File and calls Sync() after every Write so that
// log lines are guaranteed to be on disk even if the process is hard-killed.
type syncWriter struct{ f *os.File }

func (w *syncWriter) Write(p []byte) (n int, err error) {
	n, err = w.f.Write(p)
	w.f.Sync() // flush OS page cache → disk on every log line
	return
}

func logError(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] ERROR: %s\n",
		time.Now().Format("2006-01-02 15:04:05.000"), msg)
	if logFile != nil {
		logFile.WriteString(line)
		logFile.Sync()
	}
}

// recoverFromPanic is called via defer in goroutines. It catches software
// panics (interface conversion, nil dereference that reached user code, etc.)
// and writes the full stack trace to error.txt before re-returning.
// Fatal runtime errors (concurrent map writes, etc.) are NOT caught here —
// they are captured by the stderr redirect set up in init().
func recoverFromPanic() {
	if r := recover(); r != nil {
		entry := fmt.Sprintf(
			"[%s] PANIC RECOVERED: %v\n--- stack trace ---\n%s-------------------\n",
			time.Now().Format("2006-01-02 15:04:05.000"),
			r,
			string(debug.Stack()),
		)
		if logFile != nil {
			logFile.WriteString(entry)
			logFile.Sync()
		}
	}
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			entry := fmt.Sprintf(
				"[%s] FATAL PANIC in main: %v\n--- stack trace ---\n%s-------------------\n",
				time.Now().Format("2006-01-02 15:04:05.000"),
				r,
				string(debug.Stack()),
			)
			if logFile != nil {
				logFile.WriteString(entry)
				logFile.Sync()
				logFile.Close()
			}
			os.Exit(1)
		}
	}()

	app := tview.NewApplication()
	pages := tview.NewPages()

	ctrl := controllers.NewAppController(app)

	loadingView := views.NewLoadingView(app)
	loginView := views.NewLoginView(app, ctrl.OnLoginSubmit)
	chatView := views.NewChatView(
		app,
		ctrl.OnSendMessage,
		ctrl.OnCommand,
	)

	ctrl.RegisterView(models.ScreenLoading, loadingView)
	ctrl.RegisterView(models.ScreenLogin, loginView)
	ctrl.RegisterView(models.ScreenChat, chatView)

	pages.AddPage("loading", loadingView.GetPrimitive(), true, true)
	pages.AddPage("login", loginView.Primitive(), true, false)
	pages.AddPage("chat", chatView.Primitive(), true, false)

	// ── LOADING ───────────────────────────────────────────────────────────────
	ctrl.SM.OnEnter(models.ScreenLoading, func() {
		defer recoverFromPanic()
		pages.SwitchToPage("loading")

		go func() {
			defer recoverFromPanic()

			steps := []struct {
				progress int
				label    string
			}{
				{10, "Initializing…"},
				{20, "Loading modules…"},
				{40, "Preparing encryption…"},
				{60, "Checking configuration…"},
				{80, "Contacting relay server…"},
				{90, "Verifying connection…"},
				{100, ""},
			}
			for _, s := range steps {
				time.Sleep(140 * time.Millisecond)
				loadingView.UpdateProgress(s.progress)
				if s.label != "" {
					loadingView.SetStatus(s.label)
				}
			}

			loadingView.SetStatus("Contacting relay server…")
			connErr := controllers.CheckServerConnectivity(controllers.DefaultServerURL)

			if connErr != nil {
				logError("Server connectivity check failed: %v", connErr)
				app.QueueUpdateDraw(func() {
					defer recoverFromPanic()
					loadingView.ShowFatalError(
						fmt.Sprintf("Server not reachable — %s", controllers.DefaultServerURL),
					)
					loadingView.SetCountdown(4)
				})

				for i := 3; i >= 0; i-- {
					time.Sleep(1 * time.Second)
					remaining := i
					app.QueueUpdateDraw(func() {
						defer recoverFromPanic()
						loadingView.SetCountdown(remaining)
					})
				}

				time.Sleep(200 * time.Millisecond)
				app.Stop()
				return
			}

			log.Printf("Server reachable at %s", controllers.DefaultServerURL)
			loadingView.SetStatus("Connected  ✓")
			time.Sleep(300 * time.Millisecond)

			app.QueueUpdateDraw(func() {
				defer recoverFromPanic()
				ctrl.SM.Transition(models.ScreenLogin)
			})
		}()
	})

	// ── LOGIN ─────────────────────────────────────────────────────────────────
	ctrl.SM.OnEnter(models.ScreenLogin, func() {
		defer recoverFromPanic()
		pages.SwitchToPage("login")
		loginView.StartUsernamePrompt()
		app.SetFocus(loginView.Primitive())
	})

	// ── CHAT ──────────────────────────────────────────────────────────────────
	ctrl.SM.OnEnter(models.ScreenChat, func() {
		defer recoverFromPanic()
		pages.SwitchToPage("chat")
		app.SetFocus(chatView.InputPrimitive())
	})

	// ── CHAT EXIT ─────────────────────────────────────────────────────────────
	ctrl.SM.OnExit(models.ScreenChat, func() {
		defer recoverFromPanic()
		ctrl.StopBot()
		if chat, ok := ctrl.Views[models.ScreenChat].(*views.ChatView); ok {
			chat.Stop()
		}
	})

	go func() {
		defer recoverFromPanic()
		time.Sleep(100 * time.Millisecond)
		app.QueueUpdateDraw(func() {
			defer recoverFromPanic()
			ctrl.SM.Transition(models.ScreenLoading)
		})
	}()

	if err := app.SetRoot(pages, true).Run(); err != nil {
		logError("Application error: %v", err)
	}

	log.Printf("Application exited cleanly")
	if logFile != nil {
		logFile.Close()
	}
}
