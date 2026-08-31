package main

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
)

const (
	ParseModePlain      ParseMode = "Plain"
	ParseModeMarkdown   ParseMode = "Markdown"
	ParseModeMarkdownV2 ParseMode = "MarkdownV2"
	ParseModeHTML       ParseMode = "HTML"

	maxTelegramMessageLength = 4096
	defaultMessageQueueSize  = 100
	defaultMaxRetries        = 3
	defaultQueueFullBehavior = QueueFullDrop
)

type ParseMode string

type QueueFullBehavior string

const (
	QueueFullDrop QueueFullBehavior = "drop"
	QueueFullFIFO QueueFullBehavior = "fifo"
)

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
	QueueFullBehavior QueueFullBehavior
	TelegramTemplate  *template.Template
	HTTPClient        *http.Client
	RetryMaxBackoff   time.Duration
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		GotifyWSURL:       os.Getenv("GOTIFY_WS_URL"),
		GotifyRESTURL:     os.Getenv("GOTIFY_REST_URL"),
		GotifyClientToken: os.Getenv("GOTIFY_CLIENT_TOKEN"),
		TelegramToken:     os.Getenv("TELEGRAM_TOKEN"),
		SubscriptionsFile: getSubscriptionsFile(),
		DefaultParseMode:  ParseModeMarkdown,
		MessageQueueSize:  defaultMessageQueueSize,
		MaxRetries:        defaultMaxRetries,
		QueueFullBehavior: defaultQueueFullBehavior,
		RetryMaxBackoff:   60 * time.Second,
		HTTPClient:        &http.Client{Timeout: 10 * time.Second},
	}

	if v := os.Getenv("TELEGRAM_CHAT_ID"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("TELEGRAM_CHAT_ID must be an integer: %w", err)
		}
		cfg.TelegramChatID = id
	}
	if v := os.Getenv("MESSAGE_QUEUE_SIZE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("MESSAGE_QUEUE_SIZE must be a positive integer, got %q", v)
		}
		cfg.MessageQueueSize = n
	}
	if v := os.Getenv("MAX_RETRIES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("MAX_RETRIES must be a positive integer, got %q", v)
		}
		cfg.MaxRetries = n
	}
	if v := os.Getenv("QUEUE_FULL_BEHAVIOR"); v != "" {
		behavior := QueueFullBehavior(strings.ToLower(strings.TrimSpace(v)))
		switch behavior {
		case QueueFullDrop, QueueFullFIFO:
			cfg.QueueFullBehavior = behavior
		default:
			return nil, fmt.Errorf("invalid QUEUE_FULL_BEHAVIOR %q: must be %q or %q", v, QueueFullDrop, QueueFullFIFO)
		}
	}
	if v := os.Getenv("PARSE_MODE"); v != "" {
		parseMode, err := parseFormat(v)
		if err != nil {
			return nil, fmt.Errorf("invalid PARSE_MODE: %w", err)
		}
		cfg.DefaultParseMode = parseMode
	}

	templateText := os.Getenv("TELEGRAM_TEMPLATE")
	if templateText == "" {
		templateText = "{{.Title}}\n\n{{.Message}}"
	} else {
		templateText = unescapeEnv(templateText)
	}
	tmpl, err := template.New("telegram").Parse(templateText)
	if err != nil {
		return nil, fmt.Errorf("invalid TELEGRAM_TEMPLATE: %w", err)
	}
	cfg.TelegramTemplate = tmpl
	return cfg, cfg.Validate()
}

func (c *Config) Validate() error {
	var missing []string
	required := map[string]string{
		"GOTIFY_WS_URL": c.GotifyWSURL, "GOTIFY_REST_URL": c.GotifyRESTURL,
		"GOTIFY_CLIENT_TOKEN": c.GotifyClientToken, "TELEGRAM_TOKEN": c.TelegramToken,
	}
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		}
	}
	if c.TelegramChatID == 0 {
		missing = append(missing, "TELEGRAM_CHAT_ID")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if c.MessageQueueSize <= 0 {
		return fmt.Errorf("MESSAGE_QUEUE_SIZE must be positive")
	}
	if c.MaxRetries <= 0 {
		return fmt.Errorf("MAX_RETRIES must be positive")
	}
	if c.QueueFullBehavior != QueueFullDrop && c.QueueFullBehavior != QueueFullFIFO {
		return fmt.Errorf("invalid queue full behavior %q", c.QueueFullBehavior)
	}
	if c.TelegramTemplate == nil {
		return fmt.Errorf("telegram template is not configured")
	}
	return nil
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

func getSubscriptionsFile() string {
	path := os.Getenv("SUBSCRIPTIONS_FILE")
	if path == "" {
		path = "subscriptions.json" // default path if not set
	}
	return path
}

func (c *Config) String() string {
	return fmt.Sprintf("GOTIFY_WS_URL: %s\nGOTIFY_REST_URL: %s\nTELEGRAM_CHAT_ID: %d\nSUBSCRIPTIONS_FILE: %s\nDEFAULT_PARSE_MODE: %s\nMESSAGE_QUEUE_SIZE: %d\nMAX_RETRIES: %d\nQUEUE_FULL_BEHAVIOR: %s\nTELEGRAM_TEMPLATE: %s",
		c.GotifyWSURL, c.GotifyRESTURL, c.TelegramChatID, c.SubscriptionsFile, c.DefaultParseMode, c.MessageQueueSize, c.MaxRetries, c.QueueFullBehavior, c.TelegramTemplate.Tree.Root.String())
}
