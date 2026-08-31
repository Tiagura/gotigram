package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/gorilla/websocket"
)

type GotifyClient struct{ app *App }

func (g *GotifyClient) FetchApps() ([]GotifyApp, error) {
	url := fmt.Sprintf("%s/application", g.app.Config.GotifyRESTURL)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Gotify-Key", g.app.Config.GotifyClientToken)

	resp, err := g.app.Config.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gotify application request returned HTTP %d", resp.StatusCode)
	}

	var apps []GotifyApp
	if err := json.NewDecoder(resp.Body).Decode(&apps); err != nil {
		return nil, err
	}
	return apps, nil
}

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

func (g *GotifyClient) Listen(ctx context.Context) {
	streamURL := fmt.Sprintf("%s/stream?token=%s", g.app.Config.GotifyWSURL, url.QueryEscape(g.app.Config.GotifyClientToken))
	sendQueue := make(chan tgbotapi.Chattable, g.app.Config.MessageQueueSize)

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
				g.app.Telegram.sendWithRetry(ctx, msg)
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

			sub, subscribed := g.app.Subscriptions.Get(msg.AppID)

			if !subscribed || msg.Priority < sub.Priority {
				continue
			}

			var buf bytes.Buffer
			if err := g.app.Config.TelegramTemplate.Execute(&buf, msg); err != nil {
				log.Printf("Failed to render template: %v", err)
				continue
			}

			for _, part := range splitTelegramText(buf.String(), maxTelegramMessageLength) {
				tg := tgbotapi.NewMessage(g.app.Config.TelegramChatID, part)
				// check if sub.ParseMode is Plain, if it is change to "" so that the Telegram API will treat it as plain text
				// else use the value of sub.ParseMode
				if string(sub.ParseMode) == string(ParseModePlain) {
					tg.ParseMode = ""
				} else {
					tg.ParseMode = string(sub.ParseMode)
				}
				switch g.app.Config.QueueFullBehavior {
				case QueueFullFIFO:
					select {
					case sendQueue <- tg:
					default:
							// Queue is full: evict the oldest queued message to make room,
							// so the queue always holds the most recent messages rather than
							// stalling the reader or silently dropping the newest one.
							select {
							case <-sendQueue:
									log.Printf("Telegram send queue full; dropping oldest queued message to make room for message from app %d", msg.AppID)
							default:
									// Queue drained concurrently by the worker between the two
									// selects above - nothing to evict, just proceed to enqueue.
							}
							select {
							case sendQueue <- tg:
							case <-ctx.Done():
									return
							}
					}
				case QueueFullDrop:
					select {
					case sendQueue <- tg:
					case <-ctx.Done():
						return
					default:
						log.Printf("Telegram send queue full; dropping message from app %d", msg.AppID)
					}
				}
			}
		}
	}
}
