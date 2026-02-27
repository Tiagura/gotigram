package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/gorilla/websocket"
)

/* -------------------
   Structs & Vars
------------------- */

type Subscription struct {
	ID       int
	Name     string
	Priority int // 0-10, default 0
}

type GotifyApp struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type GotifyMessage struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	AppID    int    `json:"appid"`
	Priority int    `json:"priority"`
}

var (
	GOTIFY_WS_URL       = mustEnv("GOTIFY_WS_URL")
	GOTIFY_REST_URL     = mustEnv("GOTIFY_REST_URL")
	GOTIFY_CLIENT_TOKEN = mustEnv("GOTIFY_CLIENT_TOKEN")
	TELEGRAM_TOKEN      = mustEnv("TELEGRAM_TOKEN")
	TELEGRAM_CHAT_ID    = mustInt64(mustEnv("TELEGRAM_CHAT_ID"))
	SUBSCRIPTIONS_FILE  = getSubscriptionsFile()

	subscriptions = make(map[int]Subscription)
	subMu         sync.RWMutex
)

/* -------------------
   Helpers
------------------- */

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("Missing env var: %s", key)
	}
	return v
}

func mustInt64(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		log.Fatal(err)
	}
	return v
}

func loadSubscriptionsFromFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("Failed to read subscriptions file %s: %v", path, err)
		// If file doesn't exist it means no subscriptions yet, so just create an empty one
		if os.IsNotExist(err) {
			log.Printf("Creating empty subscriptions file at %s", path)
			err = os.WriteFile(path, []byte("[]"), 0644)
			if err != nil {
				log.Printf("Failed to create empty subscriptions file at %s: %v", path, err)
			}
		}
		return
	}

	var subs []Subscription
	if err := json.Unmarshal(data, &subs); err != nil {
		log.Printf("Failed to parse subscriptions JSON from %s: %v", path, err)
		return
	}

	// Fetch current Gotify applications
	apps, err := fetchApps()
	if err != nil {
		log.Printf("Failed to fetch Gotify apps while loading subscriptions: %v", err)
		return
	}

	// Build a map for quick lookup
	appIDs := make(map[int]string)
	for _, app := range apps {
		appIDs[app.ID] = app.Name
	}

	subMu.Lock()
	defer subMu.Unlock()

	added := 0
	for _, sub := range subs {
		// Validate priority
		if sub.Priority < 0 || sub.Priority > 10 {
			log.Printf("Invalid priority %d for app ID %d in subscriptions file", sub.Priority, sub.ID)
			continue
		}

		// Check if app ID exists
		name, exists := appIDs[sub.ID]
		if !exists {
			log.Printf("Skipping subscription for unknown app ID %d", sub.ID)
			continue
		}

		subscriptions[sub.ID] = Subscription{
			ID:       sub.ID,
			Name:     name, // ensure we use the correct name from Gotify
			Priority: sub.Priority,
		}
		added++
	}

	log.Printf("Loaded %d subscriptions from %s", added, path)
}

func getSubscriptionsFile() string {
	path := os.Getenv("SUBSCRIPTIONS_FILE")
	if path == "" {
		path = "subscriptions.json" // default path if not set
	}
	return path
}

/* -------------------
   Main
------------------- */

func main() {
	bot, err := tgbotapi.NewBotAPI(TELEGRAM_TOKEN)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Authorized as %s", bot.Self.UserName)

	log.Printf("Subscriptions file: %s", SUBSCRIPTIONS_FILE)

	// Load subscriptions from file if specified
	if SUBSCRIPTIONS_FILE != "" {
		loadSubscriptionsFromFile(SUBSCRIPTIONS_FILE)
	}

	// Start WebSocket listener concurrently
	go listenGotify(bot)

	// Start Telegram bot
	startTelegram(bot)
}

/* -------------------
   Telegram Bot
------------------- */

const helpText = `/apps
/subscribe <app_id|all>[,<priority, default 0>]
/subscriptions
/unsubscribe <app_id|app_id1,app_id2,...|all>
/import <json_array>
/export
/save
`

func startTelegram(bot *tgbotapi.BotAPI) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil || !update.Message.IsCommand() {
			continue
		}

		switch update.Message.Command() {
		case "start":
			reply(bot, update, "Hi! I'm Gotigram.\nUse /help to see commands.")
		case "help":
			reply(bot, update, helpText)
		case "subscribe":
			handleSubscribe(bot, update)
		case "unsubscribe":
			handleUnsubscribe(bot, update)
		case "subscriptions":
			handleSubscriptions(bot, update)
		case "apps":
			handleApps(bot, update)
		case "import":
			handleImport(bot, update)
		case "export":
			handleExport(bot, update)
		case "save":
			handleSave(bot, update)
		default:
			reply(bot, update, "Unknown command. Use /help for a list of commands.")
		}
	}
}

func reply(bot *tgbotapi.BotAPI, update tgbotapi.Update, text string) {
	msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Failed to send reply: %v", err)
	}
}

func sendWithRetry(bot *tgbotapi.BotAPI, msg tgbotapi.Chattable, maxRetries int) {
	if maxRetries <= 0 {
		maxRetries = 1
	}
	backoff := time.Second
	var err error
	for i := 0; i < maxRetries; i++ {
		if _, err = bot.Send(msg); err == nil {
			return
		}
		log.Printf("Telegram send failed (attempt %d/%d): %v", i+1, maxRetries, err)
		// Don't retry client errors (4xx) — they will never succeed
		var apiErr *tgbotapi.Error
		if errors.As(err, &apiErr) && apiErr.Code >= 400 && apiErr.Code < 500 {
			return
		}
		if i < maxRetries-1 {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	log.Printf("Telegram send failed after %d attempts: %v", maxRetries, err)
}

/* -------------------
   Commands Handlers
------------------- */

func handleSubscribe(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	arg := strings.TrimSpace(update.Message.CommandArguments())
	if arg == "" {
		reply(bot, update, "Usage: /subscribe <app_id|all>[,<priority>]")
		return
	}

	// Split "<target>[,<priority>]"
	parts := strings.SplitN(arg, ",", 2)
	target := strings.TrimSpace(parts[0])

	priority := 0
	if len(parts) == 2 {
		p, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || p < 0 || p > 10 {
			reply(bot, update, "Priority must be an integer between 0 and 10")
			return
		}
		priority = p
	} else if len(parts) == 1 && target != "all" {
		priority = 0 // default for single app
	}

	apps, err := fetchApps()
	if err != nil {
		reply(bot, update, "Failed to fetch apps")
		return
	}

	subMu.Lock()
	defer subMu.Unlock()

	addOrUpdate := func(id int, name string) string {
		sub, ok := subscriptions[id]
		if ok {
			if sub.Priority == priority {
				return fmt.Sprintf("Already subscribed to %s (ID %d) with priority %d", name, id, priority)
			}
			sub.Priority = priority
			subscriptions[id] = sub
			return fmt.Sprintf("Updated priority of %s (ID %d) to %d", name, id, priority)
		}
		subscriptions[id] = Subscription{ID: id, Name: name, Priority: priority}
		return fmt.Sprintf("Subscribed to %s (ID %d) with priority %d", name, id, priority)
	}

	if strings.EqualFold(target, "all") {
		var messages []string
		for _, app := range apps {
			messages = append(messages, addOrUpdate(app.ID, app.Name))
		}
		reply(bot, update, strings.Join(messages, "\n"))
		return
	}

	appID, err := strconv.Atoi(target)
	if err != nil || appID <= 0 {
		reply(bot, update, "Invalid app ID")
		return
	}

	var appName string
	for _, a := range apps {
		if a.ID == appID {
			appName = a.Name
			break
		}
	}

	if appName == "" {
		reply(bot, update, "Application not found")
		return
	}

	reply(bot, update, addOrUpdate(appID, appName))
}

func handleUnsubscribe(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	arg := strings.TrimSpace(update.Message.CommandArguments())
	if arg == "" {
		reply(bot, update, "Usage: /unsubscribe <app_id|id1,id2,...|all>")
		return
	}

	subMu.Lock()
	defer subMu.Unlock()

	remove := func(id int) string {
		sub, ok := subscriptions[id]
		if !ok {
			return fmt.Sprintf("You are not subscribed to application ID %d", id)
		}
		delete(subscriptions, id)
		return fmt.Sprintf("Unsubscribed from %s (ID %d)", sub.Name, id)
	}

	// Case: /unsubscribe all
	if strings.EqualFold(arg, "all") {
		if len(subscriptions) == 0 {
			reply(bot, update, "You have no subscriptions to remove")
			return
		}

		var messages []string
		for id := range subscriptions {
			messages = append(messages, remove(id))
		}

		reply(bot, update, strings.Join(messages, "\n"))
		return
	}

	// Case: /unsubscribe id1,id2,...
	parts := strings.Split(arg, ",")
	var messages []string

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		id, err := strconv.Atoi(part)
		if err != nil || id <= 0 {
			messages = append(messages, fmt.Sprintf("Invalid app ID: %s", part))
			continue
		}

		messages = append(messages, remove(id))
	}

	reply(bot, update, strings.Join(messages, "\n"))
}

func handleSubscriptions(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	subMu.RLock()
	if len(subscriptions) == 0 {
		subMu.RUnlock()
		reply(bot, update, "You are not subscribed to any applications.")
		return
	}

	// Copy map to avoid holding lock while calling fetchApps
	subsCopy := make(map[int]Subscription, len(subscriptions))
	for id, sub := range subscriptions {
		subsCopy[id] = sub
	}
	subMu.RUnlock()

	apps, err := fetchApps()
	if err != nil {
		reply(bot, update, "Failed to fetch apps")
		return
	}

	appDict := make(map[int]string)
	for _, app := range apps {
		appDict[app.ID] = app.Name
	}

	var lines []string
	for id, sub := range subsCopy {
		name := appDict[id]
		if name == "" {
			name = "Unknown"
		}
		lines = append(lines, fmt.Sprintf("%d: %s (priority %d)", id, name, sub.Priority))
	}

	reply(bot, update, "Current subscriptions:\n"+strings.Join(lines, "\n"))
}

func handleApps(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	apps, err := fetchApps()
	if err != nil || len(apps) == 0 {
		reply(bot, update, "No available applications found.")
		return
	}

	subMu.RLock()
	defer subMu.RUnlock()

	var lines []string
	for _, app := range apps {
		status := "Not subscribed"
		if sub, ok := subscriptions[app.ID]; ok {
			status = fmt.Sprintf("Subscribed (priority %d)", sub.Priority)
		}
		lines = append(lines, fmt.Sprintf("%d: %s -> %s", app.ID, app.Name, status))
	}

	reply(bot, update, "Available applications:\n"+strings.Join(lines, "\n"))
}

func handleImport(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	arg := strings.TrimSpace(update.Message.CommandArguments())
	if arg == "" {
		reply(bot, update, "Usage: /import <JSON array of subscriptions>")
		return
	}

	var subs []Subscription
	if err := json.Unmarshal([]byte(arg), &subs); err != nil {
		reply(bot, update, "Invalid JSON: "+err.Error())
		return
	}

	// Fetch current Gotify applications
	apps, err := fetchApps()
	if err != nil {
		reply(bot, update, "Failed to fetch Gotify apps: "+err.Error())
		return
	}

	// Build a map of valid app IDs and names
	appIDs := make(map[int]string)
	for _, app := range apps {
		appIDs[app.ID] = app.Name
	}

	subMu.Lock()
	defer subMu.Unlock()

	added := 0
	var warnings []string

	for _, sub := range subs {
		if sub.ID <= 0 {
			warnings = append(warnings, fmt.Sprintf("Skipping invalid app ID %d", sub.ID))
			continue
		}

		// Check if app exists
		name, exists := appIDs[sub.ID]
		if !exists {
			warnings = append(warnings, fmt.Sprintf("Skipping unknown app ID %d", sub.ID))
			continue
		}

		// Validate priority
		if sub.Priority < 0 || sub.Priority > 10 {
			warnings = append(warnings, fmt.Sprintf("Priority %d for app ID %d is invalid, setting to 0", sub.Priority, sub.ID))
			sub.Priority = 0
		}

		subscriptions[sub.ID] = Subscription{
			ID:       sub.ID,
			Name:     name, // use Gotify app name
			Priority: sub.Priority,
		}
		added++
	}

	msg := fmt.Sprintf("Imported %d subscriptions successfully.", added)
	if len(warnings) > 0 {
		msg += "\nWarnings:\n" + strings.Join(warnings, "\n")
	}

	reply(bot, update, msg)
}

func handleExport(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	subMu.RLock()
	if len(subscriptions) == 0 {
		subMu.RUnlock()
		reply(bot, update, "There are no subscriptions to export.")
		return
	}

	// Copy to slice for stable export
	export := make([]Subscription, 0, len(subscriptions))
	for _, sub := range subscriptions {
		export = append(export, sub)
	}
	subMu.RUnlock()

	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		reply(bot, update, "Failed to export subscriptions.")
		return
	}

	msg := tgbotapi.NewMessage(
		update.Message.Chat.ID,
		"```json\n"+string(data)+"\n```",
	)
	msg.ParseMode = "Markdown"

	if _, err := bot.Send(msg); err != nil {
		log.Printf("Failed to send export message: %v", err)
	}
}

func handleSave(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	subMu.RLock()
	defer subMu.RUnlock()

	if len(subscriptions) == 0 {
		reply(bot, update, "No subscriptions to save.")
		return
	}

	// Create slice for JSON
	var subs []Subscription
	for _, sub := range subscriptions {
		subs = append(subs, sub)
	}

	data, err := json.MarshalIndent(subs, "", "  ")
	if err != nil {
		reply(bot, update, fmt.Sprintf("Failed to serialize subscriptions: %v", err))
		return
	}

	err = os.WriteFile(SUBSCRIPTIONS_FILE, data, 0644)
	if err != nil {
		reply(bot, update, fmt.Sprintf("Failed to save subscriptions to %s: %v", SUBSCRIPTIONS_FILE, err))
		return
	}

	reply(bot, update, fmt.Sprintf("Saved %d subscriptions to %s", len(subs), SUBSCRIPTIONS_FILE))
}

/* -------------------
   Gotify REST API
------------------- */

func fetchApps() ([]GotifyApp, error) {
	url := fmt.Sprintf("%s/application", GOTIFY_REST_URL)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Gotify-Key", GOTIFY_CLIENT_TOKEN)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gotify returned status %d", resp.StatusCode)
	}

	var apps []GotifyApp
	if err := json.NewDecoder(resp.Body).Decode(&apps); err != nil {
		return nil, err
	}
	return apps, nil
}

/* -------------------
   Gotify WebSocket
------------------- */

func listenGotify(bot *tgbotapi.BotAPI) {
	url := fmt.Sprintf("%s/stream?token=%s", GOTIFY_WS_URL, GOTIFY_CLIENT_TOKEN)

	// Buffered send queue: decouples WS reading from Telegram sending,
	// caps memory usage, and preserves message order.
	sendQueue := make(chan tgbotapi.Chattable, 100)
	defer close(sendQueue)
	go func() {
		for msg := range sendQueue {
			sendWithRetry(bot, msg, 3)
		}
	}()

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		log.Fatalf("Failed to connect to Gotify WS: %v", err)
	}
	defer conn.Close()

	log.Println("Connected to Gotify stream")

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("WebSocket error: %v", err)
			return
		}

		var msg GotifyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("Error parsing message: %v", err)
			continue
		}

		log.Printf("Gotify message from app %d: %s - %s (priority %d)", msg.AppID, msg.Title, msg.Message, msg.Priority)

		subMu.RLock()
		sub, subscribed := subscriptions[msg.AppID]
		subMu.RUnlock()

		if subscribed {
			if msg.Priority >= sub.Priority {
				log.Printf("Forwarding message from app %d (sub prio %d)", msg.AppID, sub.Priority)
				text := fmt.Sprintf("%s - %s", msg.Title, msg.Message)
				tg := tgbotapi.NewMessage(TELEGRAM_CHAT_ID, text)
				select {
				case sendQueue <- tg:
				default:
					log.Printf("Telegram send queue full, dropping message from app %d", msg.AppID)
				}
			} else {
				log.Printf("Message priority %d < subscription priority %d, ignoring", msg.Priority, sub.Priority)
			}
		} else {
			log.Printf("Message from unsubscribed app %d, ignoring", msg.AppID)
		}
	}
}
