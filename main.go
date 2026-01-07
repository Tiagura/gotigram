package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

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

/* -------------------
   Main
------------------- */

func main() {
	bot, err := tgbotapi.NewBotAPI(TELEGRAM_TOKEN)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Authorized as %s", bot.Self.UserName)

	// Start WebSocket listener concurrently
	go listenGotify(bot)

	// Start Telegram bot
	startTelegram(bot)
}

/* -------------------
   Telegram Bot
------------------- */

const helpText = `/subscribe <app_id>,<priority, default 0>
/subscribe all,<priority, default 0>
/unsubscribe <app_id>
/unsubscribe all
/subscriptions
/apps
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
		default:
			reply(bot, update, "Unknown command. Use /help for a list of commands.")
		}
	}
}

func reply(bot *tgbotapi.BotAPI, update tgbotapi.Update, text string) {
	msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
	bot.Send(msg)
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
		reply(bot, update, "Usage: /unsubscribe <app_id|all>")
		return
	}

	subMu.Lock()
	defer subMu.Unlock()

	remove := func(id int, name string) string {
		if _, ok := subscriptions[id]; !ok {
			return fmt.Sprintf("You are not subscribed to %s (ID %d)", name, id)
		}
		delete(subscriptions, id)
		return fmt.Sprintf("Unsubscribed from %s (ID %d)", name, id)
	}

	if strings.EqualFold(arg, "all") {
		if len(subscriptions) == 0 {
			reply(bot, update, "You have no subscriptions to remove")
			return
		}
		var messages []string
		for _, sub := range subscriptions {
			messages = append(messages, remove(sub.ID, sub.Name))
		}
		reply(bot, update, strings.Join(messages, "\n"))
		return
	}

	appID, err := strconv.Atoi(arg)
	if err != nil || appID <= 0 {
		reply(bot, update, "Invalid app ID")
		return
	}

	sub, ok := subscriptions[appID]
	if !ok {
		reply(bot, update, fmt.Sprintf("You are not subscribed to application ID %d", appID))
		return
	}

	reply(bot, update, remove(appID, sub.Name))
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
				bot.Send(tg)
			} else {
				log.Printf("Message priority %d < subscription priority %d, ignoring", msg.Priority, sub.Priority)
			}
		} else {
			log.Printf("Message from unsubscribed app %d, ignoring", msg.AppID)
		}
	}
}
