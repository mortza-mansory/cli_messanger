package controllers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rivo/tview"
)

var DefaultServerURL = "http://tccbackend-production-831d.up.railway.app"

const serverAccessKey = "secure_chat_key_2024"

// ── Wire types ────────────────────────────────────────────────────────────────

type sendRequest struct {
	AccessKey string `json:"access_key"`
	ClientID  string `json:"client_id"`
	Username  string `json:"username"`
	Content   string `json:"content"`
	Color     string `json:"color"`
}

type sendResponse struct {
	Status string `json:"status"`
	ID     string `json:"id"`
	Time   string `json:"time"`
}

type pollMessage struct {
	Username  string
	Content   string
	Color     string
	ID        string
	Timestamp time.Time
}

var knownPollKeys = map[string]bool{
	"color":     true,
	"id":        true,
	"timestamp": true,
}

// parsePollMessages parses the raw JSON array from /api/poll.
// Logs every step so the last line before a crash identifies the bad message.
func parsePollMessages(data []byte) ([]*pollMessage, error) {
	log.Printf("TRACE parsePollMessages: raw body (%d bytes): %.500s", len(data), data)

	var rawList []map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawList); err != nil {
		log.Printf("TRACE parsePollMessages: unmarshal error: %v", err)
		return nil, fmt.Errorf("parse poll array: %w", err)
	}
	log.Printf("TRACE parsePollMessages: parsed %d entries", len(rawList))

	msgs := make([]*pollMessage, 0, len(rawList))
	for i, raw := range rawList {
		log.Printf("TRACE parsePollMessages: entry[%d] keys=%v", i, mapKeys(raw))
		msg := &pollMessage{}

		if v, ok := raw["color"]; ok {
			json.Unmarshal(v, &msg.Color)
		}
		if v, ok := raw["id"]; ok {
			json.Unmarshal(v, &msg.ID)
		}
		if v, ok := raw["timestamp"]; ok {
			json.Unmarshal(v, &msg.Timestamp)
		}

		for key, val := range raw {
			if knownPollKeys[key] {
				continue
			}
			msg.Username = key
			json.Unmarshal(val, &msg.Content)
			break
		}

		log.Printf("TRACE parsePollMessages: entry[%d] id=%q user=%q color=%q content=%.80q",
			i, msg.ID, msg.Username, msg.Color, msg.Content)

		if msg.Username == "" || msg.Content == "" || msg.ID == "" {
			log.Printf("TRACE parsePollMessages: entry[%d] SKIPPED (malformed)", i)
			continue
		}
		msgs = append(msgs, msg)
	}
	log.Printf("TRACE parsePollMessages: returning %d valid messages", len(msgs))
	return msgs, nil
}

func mapKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ── NetworkClient ─────────────────────────────────────────────────────────────

type NetworkClient struct {
	serverURL string
	clientID  string
	app       *tview.Application

	httpClient *http.Client
	stopped    int32
	stopCh     chan struct{}

	lastIDMu sync.Mutex
	lastID   string

	sentIDsMu sync.Mutex
	sentIDs   map[string]struct{}

	onMessage      func(username, content, colorTag string)
	onStatusChange func(connected bool, msg string)
}

func NewNetworkClient(
	app *tview.Application,
	serverURL string,
	onMessage func(username, content, colorTag string),
	onStatusChange func(connected bool, msg string),
) *NetworkClient {
	cid := generateClientID()
	log.Printf("TRACE NewNetworkClient: url=%s clientID=%s", serverURL, cid)
	return &NetworkClient{
		serverURL:      serverURL,
		clientID:       cid,
		app:            app,
		httpClient:     &http.Client{Timeout: 40 * time.Second},
		stopCh:         make(chan struct{}),
		sentIDs:        make(map[string]struct{}),
		onMessage:      onMessage,
		onStatusChange: onStatusChange,
	}
}

func generateClientID() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return fmt.Sprintf("client_%d", r.Int63n(1_000_000_000))
}

func (nc *NetworkClient) Start() {
	log.Printf("TRACE NetworkClient.Start: launching pollLoop goroutine")
	go nc.pollLoop()
}

func (nc *NetworkClient) SendMessage(username, content, colorTag string) {
	if atomic.LoadInt32(&nc.stopped) == 1 {
		return
	}
	log.Printf("TRACE NetworkClient.SendMessage: user=%q content=%.60q color=%q", username, content, colorTag)
	go nc.sendAsync(username, content, colorTag)
}

func (nc *NetworkClient) Stop() {
	if atomic.CompareAndSwapInt32(&nc.stopped, 0, 1) {
		log.Printf("TRACE NetworkClient.Stop: closing stopCh")
		close(nc.stopCh)
	}
}

// ServerURL returns the relay server base URL this client is connected to.
func (nc *NetworkClient) ServerURL() string {
	return nc.serverURL
}

// ── Send ──────────────────────────────────────────────────────────────────────

func (nc *NetworkClient) sendAsync(username, content, colorTag string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC NetworkClient.sendAsync: %v", r)
		}
	}()

	log.Printf("TRACE sendAsync: building request user=%q content=%.60q", username, content)
	body := sendRequest{
		AccessKey: serverAccessKey,
		ClientID:  nc.clientID,
		Username:  username,
		Content:   content,
		Color:     colorTag,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		log.Printf("TRACE sendAsync: marshal error: %v", err)
		return
	}

	log.Printf("TRACE sendAsync: POST %s/api/send", nc.serverURL)
	resp, err := nc.httpClient.Post(
		nc.serverURL+"/api/send",
		"application/json",
		bytes.NewReader(bodyJSON),
	)
	if err != nil {
		log.Printf("TRACE sendAsync: POST error: %v", err)
		nc.notifyStatus(false, "Message send failed — server unreachable.")
		return
	}
	defer resp.Body.Close()
	log.Printf("TRACE sendAsync: POST status=%d", resp.StatusCode)

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		nc.notifyStatus(false, "Server rejected access key.")
	case http.StatusOK, http.StatusCreated:
		var sr sendResponse
		if err := json.NewDecoder(resp.Body).Decode(&sr); err == nil && sr.ID != "" {
			log.Printf("TRACE sendAsync: server assigned id=%q", sr.ID)
			nc.sentIDsMu.Lock()
			nc.sentIDs[sr.ID] = struct{}{}
			nc.sentIDsMu.Unlock()
		}
	default:
		raw, _ := io.ReadAll(resp.Body)
		log.Printf("TRACE sendAsync: unexpected status %d body=%.120s", resp.StatusCode, raw)
	}
}

// ── Poll loop ─────────────────────────────────────────────────────────────────

func (nc *NetworkClient) pollLoop() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC NetworkClient.pollLoop: %v", r)
		}
	}()

	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second
	firstConnect := true
	wasConnected := false
	iteration := 0

	for {
		iteration++
		if atomic.LoadInt32(&nc.stopped) == 1 {
			log.Printf("TRACE pollLoop[%d]: stopped, exiting", iteration)
			return
		}

		log.Printf("TRACE pollLoop[%d]: calling poll(), lastID=%q", iteration, nc.lastID)
		msgs, err := nc.poll()
		if err != nil {
			log.Printf("TRACE pollLoop[%d]: poll error: %v", iteration, err)
			if firstConnect {
				nc.notifyStatus(false, fmt.Sprintf("Cannot reach server at %s", nc.serverURL))
			} else if wasConnected {
				nc.notifyStatus(false, fmt.Sprintf("Connection lost — reconnecting in %v…", backoff))
			}
			wasConnected = false
			select {
			case <-nc.stopCh:
				return
			case <-time.After(backoff):
			}
			backoff = minDur(backoff*2, maxBackoff)
			continue
		}

		if firstConnect || !wasConnected {
			nc.notifyStatus(true, fmt.Sprintf("Connected to relay at %s", nc.serverURL))
		}
		backoff = 1 * time.Second
		firstConnect = false
		wasConnected = true

		log.Printf("TRACE pollLoop[%d]: poll returned %d messages (nil=%v)", iteration, len(msgs), msgs == nil)

		for idx, msg := range msgs {
			log.Printf("TRACE pollLoop[%d]: dispatching msg[%d] id=%q user=%q color=%q content=%.80q",
				iteration, idx, msg.ID, msg.Username, msg.Color, msg.Content)
			nc.handleIncoming(msg)
			log.Printf("TRACE pollLoop[%d]: msg[%d] dispatch complete", iteration, idx)
		}

		if msgs == nil {
			select {
			case <-nc.stopCh:
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

func (nc *NetworkClient) poll() ([]*pollMessage, error) {
	nc.lastIDMu.Lock()
	lastID := nc.lastID
	nc.lastIDMu.Unlock()

	params := url.Values{}
	params.Set("access_key", serverAccessKey)
	params.Set("client_id", nc.clientID)
	if lastID != "" {
		params.Set("last_id", lastID)
	}

	log.Printf("TRACE poll: GET %s/api/poll lastID=%q", nc.serverURL, lastID)
	req, err := http.NewRequest(http.MethodGet, nc.serverURL+"/api/poll?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := nc.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	log.Printf("TRACE poll: response status=%d", resp.StatusCode)

	switch resp.StatusCode {
	case http.StatusNoContent:
		log.Printf("TRACE poll: 204 no content")
		return nil, nil

	case http.StatusUnauthorized:
		return nil, fmt.Errorf("server rejected access key")

	case http.StatusOK:
		rawBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read poll body: %w", err)
		}
		log.Printf("TRACE poll: 200 body=%d bytes", len(rawBody))
		msgs, err := parsePollMessages(rawBody)
		if err != nil {
			return nil, err
		}
		if len(msgs) > 0 {
			nc.lastIDMu.Lock()
			nc.lastID = msgs[len(msgs)-1].ID
			nc.lastIDMu.Unlock()
			log.Printf("TRACE poll: advanced lastID to %q", nc.lastID)
		}
		return msgs, nil

	default:
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected HTTP %d: %.120s", resp.StatusCode, body)
	}
}

func (nc *NetworkClient) handleIncoming(msg *pollMessage) {
	log.Printf("TRACE handleIncoming: checking sentIDs for id=%q", msg.ID)
	nc.sentIDsMu.Lock()
	_, isMine := nc.sentIDs[msg.ID]
	if isMine {
		delete(nc.sentIDs, msg.ID)
	}
	nc.sentIDsMu.Unlock()

	if isMine {
		log.Printf("TRACE handleIncoming: id=%q is mine, skipping echo", msg.ID)
		return
	}

	log.Printf("TRACE handleIncoming: calling onMessage user=%q color=%q content=%.80q",
		msg.Username, msg.Color, msg.Content)
	if nc.onMessage != nil {
		nc.onMessage(msg.Username, msg.Content, msg.Color)
	}
	log.Printf("TRACE handleIncoming: onMessage returned for id=%q", msg.ID)
}

func (nc *NetworkClient) notifyStatus(connected bool, msg string) {
	log.Printf("TRACE notifyStatus: connected=%v msg=%q", connected, msg)
	if nc.onStatusChange != nil {
		nc.onStatusChange(connected, msg)
	}
}

// ── Startup connectivity check ────────────────────────────────────────────────

func CheckServerConnectivity(serverURL string) error {
	log.Printf("TRACE CheckServerConnectivity: GET %s/health", serverURL)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(serverURL + "/health")
	if err != nil {
		log.Printf("TRACE CheckServerConnectivity: error: %v", err)
		return fmt.Errorf("relay server not available at %s: %w", serverURL, err)
	}
	resp.Body.Close()
	log.Printf("TRACE CheckServerConnectivity: status=%d", resp.StatusCode)
	if resp.StatusCode >= 500 {
		return fmt.Errorf("relay server returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// ── Server stats ──────────────────────────────────────────────────────────────

// ServerStats mirrors the /api/stats response.
type ServerStats struct {
	ChatStats struct {
		TotalMessages  int `json:"total_messages"`
		WaitingClients int `json:"waiting_clients"`
		MaxWaiters     int `json:"max_waiters"`
	} `json:"chat_stats"`
	ActiveClients int    `json:"active_clients"`
	Status        string `json:"status"`
}

// FetchStats calls GET /api/stats and returns the parsed result.
// Uses a short 5-second timeout — stats are non-critical, failure is silent.
func (nc *NetworkClient) FetchStats() (*ServerStats, error) {
	params := url.Values{}
	params.Set("access_key", serverAccessKey)
	params.Set("client_id", nc.clientID)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(nc.serverURL + "/api/stats?" + params.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stats HTTP %d", resp.StatusCode)
	}

	var stats ServerStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decode stats: %w", err)
	}
	return &stats, nil
}
