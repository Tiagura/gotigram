package main

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"sync"
)

type Subscription struct {
	ID        int
	Name      string
	Priority  int
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

type SubscriptionStore struct {
	mu    sync.RWMutex
	items map[int]Subscription
}

func NewSubscriptionStore() *SubscriptionStore {
	return &SubscriptionStore{items: make(map[int]Subscription)}
}
func (s *SubscriptionStore) Get(id int) (Subscription, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.items[id]
	return v, ok
}
func (s *SubscriptionStore) Snapshot() map[int]Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[int]Subscription, len(s.items))
	for k, v := range s.items {
		out[k] = v
	}
	return out
}
func (s *SubscriptionStore) Set(sub Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[sub.ID] = sub
}
func (s *SubscriptionStore) Delete(id int) (Subscription, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.items[id]
	if ok {
		delete(s.items, id)
	}
	return v, ok
}
func (s *SubscriptionStore) Replace(items map[int]Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = items
}
func (s *SubscriptionStore) Len() int { s.mu.RLock(); defer s.mu.RUnlock(); return len(s.items) }

type App struct {
	Config        *Config
	Subscriptions *SubscriptionStore
	Telegram      *TelegramService
	Gotify        *GotifyClient
}

func NewApp(cfg *Config, bot *tgbotapi.BotAPI) *App {
	store := NewSubscriptionStore()
	app := &App{Config: cfg, Subscriptions: store}
	app.Telegram = &TelegramService{app: app, bot: bot}
	app.Gotify = &GotifyClient{app: app}
	return app
}
