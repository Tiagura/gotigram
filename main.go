package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/gorilla/websocket"
)

/*
	-------------------
	  Structs & Vars

-------------------
*/

const maxTelegramMessageLength = 4096 // Telegram's API maximum message length

const (
	ParseModePlain      ParseMode = ""
	ParseModeMarkdown   ParseMode = "Markdown"
	ParseModeMarkdownV2 ParseMode = "MarkdownV2"
	ParseModeHTML       ParseMode = "HTML"
)

type ParseMode string

type Subscription struct {
	ID        int
	Name      string
	Priority  int // 0-10, default 0
	ParseMode ParseMode
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
	GOTIFY_WS_URL              = mustEnv("GOTIFY_WS_URL")
	GOTIFY_REST_URL            = mustEnv("GOTIFY_REST_URL")
	GOTIFY_CLIENT_TOKEN        = mustEnv("GOTIFY_CLIENT_TOKEN")
	TELEGRAM_TOKEN             = mustEnv("TELEGRAM_TOKEN")
	TELEGRAM_CHAT_ID           = mustInt64(mustEnv("TELEGRAM_CHAT_ID"))
	SUBSCRIPTIONS_FILE         = getSubscriptionsFile()
	PARSE_MODE                 = getDefaultFormat()
	DEFAULT_MESSAGE_QUEUE_SIZE = 100
	MAX_RETRIES                = 3

	httpClient = &http.Client{Timeout: 10 * time.Second}

	subscriptions = make(map[int]Subscription)
	subMu         sync.RWMutex

	telegramTemplate = func() *template.Template {
		tmplStr := os.Getenv("TELEGRAM_TEMPLATE")
		if tmplStr == "" {
			tmplStr = "{{.Title}}\n\n{{.Message}}"
		} else {
			tmplStr = unescapeEnv(tmplStr)
		}

		log.Printf("Parsed Telegram template: %s", tmplStr)

		t, err := template.New("telegram").Parse(tmplStr)
		if err != nil {
			log.Fatalf("Failed to parse TELEGRAM_TEMPLATE: %v", err)
		}
		return t
	}()

	messageQueueSize = func() int {

		sizeStr := os.Getenv("MESSAGE_QUEUE_SIZE")
		if sizeStr == "" {
			return DEFAULT_MESSAGE_QUEUE_SIZE
		}

		size, err := strconv.Atoi(sizeStr)
		if err != nil || size <= 0 {
			log.Printf("Invalid MESSAGE_QUEUE_SIZE value: %q, must be > 0. Defaulting to %d", sizeStr, DEFAULT_MESSAGE_QUEUE_SIZE)
			return DEFAULT_MESSAGE_QUEUE_SIZE
		}

		return size
	}()

	maxRetries = func() int {

		retriesStr := os.Getenv("MAX_RETRIES")
		if retriesStr == "" {
			return MAX_RETRIES
		}

		retries, err := strconv.Atoi(retriesStr)
		if err != nil || retries <= 0 {
			log.Printf("Invalid MAX_RETRIES value: %q, must be > 0. Defaulting to %d", retriesStr, MAX_RETRIES)
			return MAX_RETRIES
		}

		return retries
	}()
)

/* -------------------
   Helpers
------------------- */

// Unescape \n, \t, etc.
func unescapeEnv(s string) string {
	replacer := strings.NewReplacer(
		`\\`, `\`, // literal backslash
		`\n`, "\n", // newline
		`\t`, "\t", // tab
	)
	return replacer.Replace(s)
}

func parseFormat(s string) (ParseMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "plain":
		return ParseModePlain, nil
	case "markdown":
		return ParseModeMarkdown, nil
	case "markdownv2":
		return ParseModeMarkdownV2, nil
	case "html":
		return ParseModeHTML, nil
	default:
		return ParseModeMarkdown, fmt.Errorf("invalid parse mode %q.", s)
	}
}

func getDefaultFormat() ParseMode {
	v := os.Getenv("PARSE_MODE")
	if v == "" {
		return "Markdown" // sensible fallback
	}
	f, err := parseFormat(v)
	if err != nil {
		log.Fatalf("Invalid PARSE_MODE: %v", err)
	}
	return f
}

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

		f := PARSE_MODE
		if sub.ParseMode != "" {
			parsed, err := parseFormat(string(sub.ParseMode))
			if err != nil {
				log.Printf("Invalid parse mode %q for app ID %d in subscriptions file, defaulting to %s", sub.ParseMode, sub.ID, PARSE_MODE)
			} else {
				f = parsed
			}
		}

		// Check if app ID exists
		name, exists := appIDs[sub.ID]
		if !exists {
			log.Printf("Skipping subscription for unknown app ID %d", sub.ID)
			continue
		}

		subscriptions[sub.ID] = Subscription{
			ID:        sub.ID,
			Name:      name, // ensure we use the correct name from Gotify
			Priority:  sub.Priority,
			ParseMode: f,
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bot, err := tgbotapi.NewBotAPI(TELEGRAM_TOKEN)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Authorized as %s", bot.Self.UserName)
	log.Printf("Subscriptions file: %s", SUBSCRIPTIONS_FILE)

	if SUBSCRIPTIONS_FILE != "" {
		loadSubscriptionsFromFile(SUBSCRIPTIONS_FILE)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		listenGotify(ctx, bot)
	}()

	startTelegram(ctx, bot)
	stop()
	wg.Wait()
	log.Println("Shutdown complete")
}

/* -------------------
   Telegram Bot
------------------- */

const helpText = `/apps
/subscribe <app_id|all> [-p <priority, default 0>] [-f <Plain|Markdown|MarkdownV2|HTML, default Markdown>]
/subscriptions
/unsubscribe <app_id|app_id1,app_id2,...|all>
/import <json_array>
/export
/save
`

func isAuthorized(update tgbotapi.Update) bool {
	return update.Message != nil && update.Message.Chat.ID == TELEGRAM_CHAT_ID
}

func startTelegram(ctx context.Context, bot *tgbotapi.BotAPI) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)
	defer bot.StopReceivingUpdates()

	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-updates:
			if !ok {
				return
			}
			if update.Message == nil || !update.Message.IsCommand() {
				continue
			}
			if !isAuthorized(update) {
				log.Printf("Ignoring command from unauthorized chat %d", update.Message.Chat.ID)
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
}

func reply(bot *tgbotapi.BotAPI, update tgbotapi.Update, text string) {
	msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Failed to send reply: %v", err)
	}
}

func sendWithRetry(ctx context.Context, bot *tgbotapi.BotAPI, msg tgbotapi.Chattable, maxRetries int) {
	if maxRetries <= 0 {
		maxRetries = 1
	}

	backoff := time.Second
	var err error

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if _, err = bot.Send(msg); err == nil {
			return
		}

		log.Printf("Telegram send failed (attempt %d/%d): %v", i+1, maxRetries, err)

		var apiErr *tgbotapi.Error
		if errors.As(err, &apiErr) && apiErr.Code >= 400 && apiErr.Code < 500 {
			if apiErr.Code == 429 && apiErr.RetryAfter > 0 {
				backoff = time.Duration(apiErr.RetryAfter) * time.Second
			} else {
				return
			}
		}

		if i < maxRetries-1 {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
			if apiErr == nil || apiErr.Code != 429 {
				backoff = min(backoff*2, 60*time.Second)
			}
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
		reply(bot, update, "Usage: /subscribe <app_id|all> [-p <priority>] [-f <Plain|Markdown|MarkdownV2|HTML>]")
		return
	}

	// Parse tokens: first token is the target, remaining are flags.
	tokens := strings.Fields(arg)
	target := tokens[0]

	priority := 0
	parseMode := PARSE_MODE

	for i := 1; i < len(tokens); i++ {
		switch tokens[i] {
		case "-p":
			if i+1 >= len(tokens) {
				reply(bot, update, "Flag -p requires a value")
				return
			}
			i++
			p, err := strconv.Atoi(tokens[i])
			if err != nil || p < 0 || p > 10 {
				reply(bot, update, "Priority must be an integer between 0 and 10")
				return
			}
			priority = p
		case "-f":
			if i+1 >= len(tokens) {
				reply(bot, update, "Flag -f requires a value")
				return
			}
			i++
			f, err := parseFormat(tokens[i])
			if err != nil {
				reply(bot, update, err.Error())
				return
			}
			parseMode = f
		default:
			reply(bot, update, fmt.Sprintf("Unknown flag %q. Usage: /subscribe <app_id|all> [-p <priority>] [-f <Plain|Markdown|MarkdownV2|HTML>]", tokens[i]))
			return
		}
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
			if sub.Priority == priority && sub.ParseMode == parseMode {
				return fmt.Sprintf("Already subscribed to %s (ID %d) with priority %d, parse mode %q", name, id, priority, parseMode)
			}
			sub.Priority = priority
			sub.ParseMode = parseMode
			subscriptions[id] = sub
			return fmt.Sprintf("Updated %s (ID %d): priority %d, parse mode %q", name, id, priority, parseMode)
		}
		subscriptions[id] = Subscription{ID: id, Name: name, Priority: priority, ParseMode: parseMode}
		return fmt.Sprintf("Subscribed to %s (ID %d) with priority %d, parse mode %q", name, id, priority, parseMode)
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

		ids := make([]int, 0, len(subscriptions))
		for id := range subscriptions {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		var messages []string
		for _, id := range ids {
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

	ids := make([]int, 0, len(subsCopy))
	for id := range subsCopy {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	var lines []string
	for _, id := range ids {
		sub := subsCopy[id]
		name := appDict[id]
		if name == "" {
			name = "Unknown"
		}
		lines = append(lines, fmt.Sprintf("%d: %s (priority %d, parse mode %q)", id, name, sub.Priority, sub.ParseMode))
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

	sort.Slice(apps, func(i, j int) bool { return apps[i].ID < apps[j].ID })
	var lines []string
	for _, app := range apps {
		status := "Not subscribed"
		if sub, ok := subscriptions[app.ID]; ok {
			status = fmt.Sprintf("Subscribed (priority %d, parse mode %q)", sub.Priority, sub.ParseMode)
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

		f := PARSE_MODE
		if sub.ParseMode != "" {
			parsed, err := parseFormat(string(sub.ParseMode))
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("Invalid parse mode %q for app ID %d, defaulting to %s", sub.ParseMode, sub.ID, PARSE_MODE))
			} else {
				f = parsed
			}
		}

		subscriptions[sub.ID] = Subscription{
			ID:        sub.ID,
			Name:      name, // use Gotify app name
			Priority:  sub.Priority,
			ParseMode: f,
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
	sort.Slice(export, func(i, j int) bool { return export[i].ID < export[j].ID })

	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		reply(bot, update, "Failed to export subscriptions.")
		return
	}

	text := "<pre>" + html.EscapeString(string(data)) + "</pre>"
	msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
	msg.ParseMode = string(ParseModeHTML)

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
	subs := make([]Subscription, 0, len(subscriptions))
	for _, sub := range subscriptions {
		subs = append(subs, sub)
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].ID < subs[j].ID })

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

	resp, err := httpClient.Do(req)
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

func splitTelegramText(text string, maxChars int) []string {
	if maxChars <= 0 || len([]rune(text)) <= maxChars {
		return []string{text}
	}

	runes := []rune(text)
	parts := make([]string, 0, (len(runes)+maxChars-1)/maxChars)
	for len(runes) > maxChars {
		cut := maxChars
		for i := maxChars - 1; i >= maxChars/2; i-- {
			if runes[i] == '\n' {
				cut = i + 1
				break
			}
		}
		parts = append(parts, string(runes[:cut]))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		parts = append(parts, string(runes))
	}
	return parts
}

func waitForContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func listenGotify(ctx context.Context, bot *tgbotapi.BotAPI) {
	streamURL := fmt.Sprintf("%s/stream?token=%s", GOTIFY_WS_URL, url.QueryEscape(GOTIFY_CLIENT_TOKEN))
	sendQueue := make(chan tgbotapi.Chattable, messageQueueSize)

	var senderWG sync.WaitGroup
	senderWG.Add(1)
	go func() {
		defer senderWG.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-sendQueue:
				if !ok {
					return
				}
				sendWithRetry(ctx, bot, msg, maxRetries)
			}
		}
	}()

	defer func() {
		close(sendQueue)
		senderWG.Wait()
	}()

	backoff := time.Second
	const maxBackoff = 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		log.Printf("Connecting to Gotify stream...")
		conn, _, err := websocket.DefaultDialer.Dial(streamURL, nil)
		if err != nil {
			log.Printf("Failed to connect to Gotify WS: %v, retrying in %v", err, backoff)
			if !waitForContext(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		log.Println("Connected to Gotify stream")
		backoff = time.Second

		connDone := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = conn.Close()
			case <-connDone:
			}
		}()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				close(connDone)
				_ = conn.Close()
				if ctx.Err() != nil {
					return
				}
				log.Printf("WebSocket error: %v, reconnecting in %v...", err, backoff)
				if !waitForContext(ctx, backoff) {
					return
				}
				backoff = min(backoff*2, maxBackoff)
				break
			}

			backoff = time.Second

			var msg GotifyMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				log.Printf("Error parsing message: %v", err)
				continue
			}

			subMu.RLock()
			sub, subscribed := subscriptions[msg.AppID]
			subMu.RUnlock()
			if !subscribed || msg.Priority < sub.Priority {
				continue
			}

			var buf bytes.Buffer
			if err := telegramTemplate.Execute(&buf, msg); err != nil {
				log.Printf("Failed to render template: %v", err)
				continue
			}

			for _, part := range splitTelegramText(buf.String(), maxTelegramMessageLength) {
				tg := tgbotapi.NewMessage(TELEGRAM_CHAT_ID, part)
				tg.ParseMode = string(sub.ParseMode)
				select {
				case sendQueue <- tg:
				case <-ctx.Done():
					return
				default:
					log.Printf("Telegram send queue full, dropping message from app %d", msg.AppID)
				}
			}
		}
	}
}
