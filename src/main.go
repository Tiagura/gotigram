package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Authorized as %s", bot.Self.UserName)
	log.Printf("Configuration:\n%s", cfg.String())

	app := NewApp(cfg, bot)
	if cfg.SubscriptionsFile != "" {
		if err := app.LoadSubscriptions(cfg.SubscriptionsFile); err != nil {
			log.Fatalf("Failed to load subscriptions: %v", err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); app.Gotify.Listen(ctx) }()

	app.Telegram.Start(ctx)
	stop()
	wg.Wait()
	log.Println("Shutdown complete")
}
