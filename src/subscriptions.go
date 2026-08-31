package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
)

func (a *App) LoadSubscriptions(path string) {
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
	apps, err := a.Gotify.FetchApps()
	if err != nil {
		log.Printf("Failed to fetch Gotify apps while loading subscriptions: %v", err)
		return
	}

	// Build a map for quick lookup
	appIDs := make(map[int]string)
	for _, app := range apps {
		appIDs[app.ID] = app.Name
	}

	added := 0
	for _, sub := range subs {
		// Validate priority
		if sub.Priority < 0 || sub.Priority > 10 {
			log.Printf("Invalid priority %d for app ID %d in subscriptions file", sub.Priority, sub.ID)
			continue
		}

		f := a.Config.DefaultParseMode
		// if parse mode is missing, use default; if it's invalid, log and use default
		// missing means nil, invalid means not one of the allowed values

		if sub.ParseMode != "" {
			parsed, err := parseFormat(string(sub.ParseMode))
			if err != nil {
				log.Printf("Invalid parse mode %q for app ID %d in subscriptions file, defaulting to %s", sub.ParseMode, sub.ID, a.Config.DefaultParseMode)
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

		a.Subscriptions.Set(Subscription{
			ID:        sub.ID,
			Name:      name, // ensure we use the correct name from Gotify
			Priority:  sub.Priority,
			ParseMode: f,
		})
		added++
	}

	log.Printf("Loaded %d subscriptions from %s", added, path)
}

func (a *App) SaveSubscriptions(path string) error {
	items := a.Subscriptions.Snapshot()
	if len(items) == 0 {
		return fmt.Errorf("no subscriptions to save")
	}
	subs := make([]Subscription, 0, len(items))
	for _, sub := range items {
		subs = append(subs, sub)
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].ID < subs[j].ID })
	data, err := json.MarshalIndent(subs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
