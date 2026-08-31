package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"
)

const (
	ParseModePlain           ParseMode = "Plain"
	ParseModeMarkdown        ParseMode = "Markdown"
	ParseModeMarkdownV2      ParseMode = "MarkdownV2"
	ParseModeHTML            ParseMode = "HTML"
	maxTelegramMessageLength           = 4096
	defaultMessageQueueSize            = 100
	defaultMaxRetries                  = 3
)

type ParseMode string

type Config struct {
	GotifyWSURL       string
	GotifyRESTURL     string
	GotifyClientToken string
	TelegramToken     string
	TelegramChatID    int64
	SubscriptionsFile string
	DefaultParseMode  ParseMode
	MessageQueueSize  int
	MaxRetries        int
	TelegramTemplate  *template.Template
	HTTPClient        *http.Client
	RetryMaxBackoff   time.Duration
}

func loadConfig() *Config {
	templateText := os.Getenv("TELEGRAM_TEMPLATE")
	if templateText == "" {
		templateText = "{{.Title}}\n\n{{.Message}}"
	} else {
		templateText = unescapeEnv(templateText)
	}
	tmpl, err := template.New("telegram").Parse(templateText)
	if err != nil {
		log.Fatalf("Failed to parse TELEGRAM_TEMPLATE: %v", err)
	}

	queueSize := envPositiveInt("MESSAGE_QUEUE_SIZE", defaultMessageQueueSize)
	maxRetries := envPositiveInt("MAX_RETRIES", defaultMaxRetries)
	parseMode := ParseModeMarkdown
	if v := os.Getenv("PARSE_MODE"); v != "" {
		parseMode, err = parseFormat(v)
		if err != nil {
			log.Fatalf("Invalid PARSE_MODE: %v", err)
		}
	}

	return &Config{
		GotifyWSURL: mustEnv("GOTIFY_WS_URL"), GotifyRESTURL: mustEnv("GOTIFY_REST_URL"),
		GotifyClientToken: mustEnv("GOTIFY_CLIENT_TOKEN"), TelegramToken: mustEnv("TELEGRAM_TOKEN"),
		TelegramChatID: mustInt64(mustEnv("TELEGRAM_CHAT_ID")), SubscriptionsFile: getSubscriptionsFile(),
		DefaultParseMode: parseMode, MessageQueueSize: queueSize, MaxRetries: maxRetries,
		TelegramTemplate: tmpl, HTTPClient: &http.Client{Timeout: 10 * time.Second}, RetryMaxBackoff: 60 * time.Second,
	}
}

func envPositiveInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("Invalid %s value %q, defaulting to %d", key, v, fallback)
		return fallback
	}
	return n
}

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
		return ParseModePlain, fmt.Errorf("invalid parse mode %q.", s)
	}
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

func getSubscriptionsFile() string {
	path := os.Getenv("SUBSCRIPTIONS_FILE")
	if path == "" {
		path = "subscriptions.json" // default path if not set
	}
	return path
}
