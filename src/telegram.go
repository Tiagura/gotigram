package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const helpText = `/apps
/subscribe <app_id|all> [-p <priority, default 0>] [-f <Plain|Markdown|MarkdownV2|HTML, default Markdown>]
/subscriptions
/unsubscribe <app_id|app_id1,app_id2,...|all>
/import <json_array>
/export
/save
`

type TelegramService struct {
	app *App
	bot *tgbotapi.BotAPI
}

func (t *TelegramService) Start(ctx context.Context) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := t.bot.GetUpdatesChan(u)
	defer t.bot.StopReceivingUpdates()
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-updates:
			if !ok || update.Message == nil || !update.Message.IsCommand() {
				continue
			}
			if update.Message.Chat.ID != t.app.Config.TelegramChatID {
				log.Printf("Ignoring command from unauthorized chat %d", update.Message.Chat.ID)
				continue
			}
			switch update.Message.Command() {
			case "start":
				t.reply(update, "Hi! I'm Gotigram.\nUse /help to see commands.")
			case "help":
				t.reply(update, helpText)
			case "subscribe":
				t.handleSubscribe(update)
			case "unsubscribe":
				t.handleUnsubscribe(update)
			case "subscriptions":
				t.handleSubscriptions(update)
			case "apps":
				t.handleApps(update)
			case "import":
				t.handleImport(update)
			case "export":
				t.handleExport(update)
			case "save":
				t.handleSave(update)
			default:
				t.reply(update, "Unknown command. Use /help for a list of commands.")
			}
		}
	}
}

func (t *TelegramService) reply(update tgbotapi.Update, text string) {
	if _, err := t.bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, text)); err != nil {
		log.Printf("Failed to send reply: %v", err)
	}
}

func (t *TelegramService) sendWithRetry(ctx context.Context, msg tgbotapi.Chattable) {
	backoff := time.Second
	for i := 0; i < t.app.Config.MaxRetries; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if _, err := t.bot.Send(msg); err == nil {
			return
		} else {
			log.Printf("Telegram send failed (attempt %d/%d): %v", i+1, t.app.Config.MaxRetries, err)
			var apiErr *tgbotapi.Error
			if errors.As(err, &apiErr) && apiErr.Code >= 400 && apiErr.Code < 500 {
				if apiErr.Code == 429 && apiErr.RetryAfter > 0 {
					backoff = time.Duration(apiErr.RetryAfter) * time.Second
				} else {
					return
				}
			}
			if i < t.app.Config.MaxRetries-1 {
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
					backoff = min(backoff*2, t.app.Config.RetryMaxBackoff)
				}
			}
		}
	}
}

func (t *TelegramService) handleSubscribe(update tgbotapi.Update) {
	arg := strings.TrimSpace(update.Message.CommandArguments())
	if arg == "" {
		t.reply(update, "Usage: /subscribe <app_id|all> [-p <priority>] [-f <Plain|Markdown|MarkdownV2|HTML>]")
		return
	}

	// Parse tokens: first token is the target, remaining are flags.
	tokens := strings.Fields(arg)
	target := tokens[0]

	priority := 0
	parseMode := t.app.Config.DefaultParseMode

	for i := 1; i < len(tokens); i++ {
		switch tokens[i] {
		case "-p":
			if i+1 >= len(tokens) {
				t.reply(update, "Flag -p requires a value")
				return
			}
			i++
			p, err := strconv.Atoi(tokens[i])
			if err != nil || p < 0 || p > 10 {
				t.reply(update, "Priority must be an integer between 0 and 10")
				return
			}
			priority = p
		case "-f":
			if i+1 >= len(tokens) {
				t.reply(update, "Flag -f requires a value")
				return
			}
			i++
			f, err := parseFormat(tokens[i])
			if err != nil {
				t.reply(update, err.Error())
				return
			}
			parseMode = f
		default:
			t.reply(update, fmt.Sprintf("Unknown flag %q. Usage: /subscribe <app_id|all> [-p <priority>] [-f <Plain|Markdown|MarkdownV2|HTML>]", tokens[i]))
			return
		}
	}

	apps, err := t.app.Gotify.FetchApps()
	if err != nil {
		t.reply(update, "Failed to fetch apps")
		return
	}

	addOrUpdate := func(id int, name string) string {
		sub, ok := t.app.Subscriptions.Get(id)
		if ok {
			if sub.Priority == priority && sub.ParseMode == parseMode {
				return fmt.Sprintf("Already subscribed to %s (ID %d) with priority %d, parse mode %q", name, id, priority, parseMode)
			}
			sub.Priority = priority
			sub.ParseMode = parseMode
			t.app.Subscriptions.Set(sub)
			return fmt.Sprintf("Updated %s (ID %d): priority %d, parse mode %q", name, id, priority, parseMode)
		}
		t.app.Subscriptions.Set(Subscription{ID: id, Name: name, Priority: priority, ParseMode: parseMode})
		return fmt.Sprintf("Subscribed to %s (ID %d) with priority %d, parse mode %q", name, id, priority, parseMode)
	}

	if strings.EqualFold(target, "all") {
		var messages []string
		for _, app := range apps {
			messages = append(messages, addOrUpdate(app.ID, app.Name))
		}
		t.reply(update, strings.Join(messages, "\n"))
		return
	}

	appID, err := strconv.Atoi(target)
	if err != nil || appID <= 0 {
		t.reply(update, "Invalid app ID")
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
		t.reply(update, "Application not found")
		return
	}

	t.reply(update, addOrUpdate(appID, appName))
}

func (t *TelegramService) handleUnsubscribe(update tgbotapi.Update) {
	arg := strings.TrimSpace(update.Message.CommandArguments())
	if arg == "" {
		t.reply(update, "Usage: /unsubscribe <app_id|id1,id2,...|all>")
		return
	}

	remove := func(id int) string {
		sub, ok := t.app.Subscriptions.Get(id)
		if !ok {
			return fmt.Sprintf("You are not subscribed to application ID %d", id)
		}
		t.app.Subscriptions.Delete(id)
		return fmt.Sprintf("Unsubscribed from %s (ID %d)", sub.Name, id)
	}

	// Case: /unsubscribe all
	if strings.EqualFold(arg, "all") {
		if t.app.Subscriptions.Len() == 0 {
			t.reply(update, "You have no subscriptions to remove")
			return
		}

		ids := make([]int, 0, t.app.Subscriptions.Len())
		for id := range t.app.Subscriptions.Snapshot() {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		var messages []string
		for _, id := range ids {
			messages = append(messages, remove(id))
		}

		t.reply(update, strings.Join(messages, "\n"))
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

	t.reply(update, strings.Join(messages, "\n"))
}

func (t *TelegramService) handleSubscriptions(update tgbotapi.Update) {

	if t.app.Subscriptions.Len() == 0 {

		t.reply(update, "You are not subscribed to any applications.")
		return
	}

	// Copy map to avoid holding lock while calling fetchApps
	subsCopy := make(map[int]Subscription, t.app.Subscriptions.Len())
	for id, sub := range t.app.Subscriptions.Snapshot() {
		subsCopy[id] = sub
	}

	apps, err := t.app.Gotify.FetchApps()
	if err != nil {
		t.reply(update, "Failed to fetch apps")
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

	t.reply(update, "Current subscriptions:\n"+strings.Join(lines, "\n"))
}

func (t *TelegramService) handleApps(update tgbotapi.Update) {
	apps, err := t.app.Gotify.FetchApps()
	if err != nil || len(apps) == 0 {
		t.reply(update, "No available applications found.")
		return
	}

	defer sort.Slice(apps, func(i, j int) bool { return apps[i].ID < apps[j].ID })
	var lines []string
	for _, app := range apps {
		status := "Not subscribed"
		if sub, ok := t.app.Subscriptions.Get(app.ID); ok {
			status = fmt.Sprintf("Subscribed (priority %d, parse mode %q)", sub.Priority, sub.ParseMode)
		}
		lines = append(lines, fmt.Sprintf("%d: %s -> %s", app.ID, app.Name, status))
	}

	t.reply(update, "Available applications:\n"+strings.Join(lines, "\n"))
}

func (t *TelegramService) handleImport(update tgbotapi.Update) {
	arg := strings.TrimSpace(update.Message.CommandArguments())
	if arg == "" {
		t.reply(update, "Usage: /import <JSON array of subscriptions>")
		return
	}

	var subs []Subscription
	if err := json.Unmarshal([]byte(arg), &subs); err != nil {
		t.reply(update, "Invalid JSON: "+err.Error())
		return
	}

	// Fetch current Gotify applications
	apps, err := t.app.Gotify.FetchApps()
	if err != nil {
		t.reply(update, "Failed to fetch Gotify apps: "+err.Error())
		return
	}

	// Build a map of valid app IDs and names
	appIDs := make(map[int]string)
	for _, app := range apps {
		appIDs[app.ID] = app.Name
	}

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

		f := t.app.Config.DefaultParseMode
		if sub.ParseMode != "" {
			parsed, err := parseFormat(string(sub.ParseMode))
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("Invalid parse mode %q for app ID %d, defaulting to %s", sub.ParseMode, sub.ID, t.app.Config.DefaultParseMode))
			} else {
				f = parsed
			}
		}

		t.app.Subscriptions.Set(Subscription{
			ID:        sub.ID,
			Name:      name, // use Gotify app name
			Priority:  sub.Priority,
			ParseMode: f,
		})
		added++
	}

	msg := fmt.Sprintf("Imported %d subscriptions successfully.", added)
	if len(warnings) > 0 {
		msg += "\nWarnings:\n" + strings.Join(warnings, "\n")
	}

	t.reply(update, msg)
}

func (t *TelegramService) handleExport(update tgbotapi.Update) {

	if t.app.Subscriptions.Len() == 0 {

		t.reply(update, "There are no subscriptions to export.")
		return
	}

	// Copy to slice for stable export
	export := make([]Subscription, 0, t.app.Subscriptions.Len())
	for _, sub := range t.app.Subscriptions.Snapshot() {
		export = append(export, sub)
	}

	sort.Slice(export, func(i, j int) bool { return export[i].ID < export[j].ID })

	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		t.reply(update, "Failed to export subscriptions.")
		return
	}

	text := "<pre>" + html.EscapeString(string(data)) + "</pre>"
	msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
	msg.ParseMode = string(ParseModeHTML)

	if _, err := t.bot.Send(msg); err != nil {
		log.Printf("Failed to send export message: %v", err)
	}
}

func (t *TelegramService) handleSave(update tgbotapi.Update) {
	if t.app.Subscriptions.Len() == 0 {
		t.reply(update, "No subscriptions to save.")
		return
	}
	if err := t.app.SaveSubscriptions(t.app.Config.SubscriptionsFile); err != nil {
		t.reply(update, fmt.Sprintf("Failed to save subscriptions to %s: %v", t.app.Config.SubscriptionsFile, err))
		return
	}
	t.reply(update, fmt.Sprintf("Saved %d subscriptions to %s", t.app.Subscriptions.Len(), t.app.Config.SubscriptionsFile))
}
