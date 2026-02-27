<p align="center">
  <img src="images/logo_no_background.png" alt="logo" width="150"/>
</p>

<h1 align="center">Gotigram</h1>

Gotigram is a standalone Telegram bot that bridges your [Gotify](https://gotify.net) notifications directly to your [Telegram](https://telegram.org) DMs. It's designed to selectively forward messages from specific Gotify applications to Telegram, giving you full control over what gets pushed.

> **Note:** Gotigram is a separate application and not a Gotify plugin.

## Table of Contents

- [Table of Contents](#table-of-contents)
- [Prerequisites](#prerequisites)
- [Environment Variables](#environment-variables)
  - [Required Environment Variables](#required-environment-variables)
  - [Optional Environment Variables](#optional-environment-variables)
- [Subscriptions Configuration File](#subscriptions-configuration-file)
  - [Enabling the Config File](#enabling-the-config-file)
  - [JSON File Format](#json-file-format)
- [How to Get Your Telegram Token and Chat ID](#how-to-get-your-telegram-token-and-chat-id)
- [Getting Started](#getting-started)
  - [Option 1: Run Locally](#option-1-run-locally)
  - [Option 2: Run via Docker](#option-2-run-via-docker)
    - [Using a local build](#using-a-local-build)
    - [Using Docker Hub image](#using-docker-hub-image)
- [Bot Usage](#bot-usage)
  - [Available Commands](#available-commands)
  - [Subscribing to Applications](#subscribing-to-applications)
    - [Example](#example)
- [License](#license)
- [Contributing](#contributing)

## Prerequisites

Before using Gotigram, ensure you have the following:

- A running **Gotify server**
- A **Telegram bot**

## Environment Variables

To connect Gotigram to these services, you must provide certain configuration values via environment variables.

### Required Environment Variables

| Variable              | Description                                                                            |
|-----------------------|----------------------------------------------------------------------------------------|
| `GOTIFY_WS_URL`       | WebSocket URL of your Gotify server (e.g., `ws://gotify.com`)                          |
| `GOTIFY_REST_URL`     | REST API URL of your Gotify server (e.g., `http://gotify.com` or `https://gotify.com`) |
| `GOTIFY_CLIENT_TOKEN` | Token from Gotify "Clients" tab or an existing one                                     |
| `TELEGRAM_TOKEN`      | Token for your Telegram bot                                                            |
| `TELEGRAM_CHAT_ID`    | Your personal Telegram chat ID                                                         |

### Optional Environment Variables

| Variable              | Description                                                                            |
|-----------------------|----------------------------------------------------------------------------------------|
| `SUBSCRIPTIONS_FILE`  | Path to a JSON file containing predefined subscriptions. Defaults to `subscriptions.json`. See [how to use](#optional-subscriptions-configuration-file). |
| `TELEGRAM_TEMPLATE` | Custom Go `text/template` for Telegram messages. Available variables: `{{.Title}}` and `{{.Message}}`. Defaults to `*{{.Title}}*\n\n{{.Message}}` (bold title, blank line, then message body). Messages are sent with Markdown parse mode. **Wrap in `'...'` or `"..."`.** | |


## Subscriptions Configuration File

Gotigram can optionally preload subscriptions at startup from a JSON configuration file. This allows you to define which applications should be subscribed automatically, without manually sending commands in Telegram after each restart.

### Enabling the Config File

Set the following environment variable: `SUBSCRIPTIONS_FILE`

- If the file exists and contains valid subscriptions, they will be loaded automatically on startup.
- If the file is missing or empty, Gotigram will start normally with no subscriptions.
- Update and save the subscriptions to a file at any time using the '/save' command.

### JSON File Format

The file must contain a JSON array of subscription objects, which have the following structure:

| Field      | Required | Description                                                                                     |
| ---------- | -------- | ----------------------------------------------------------------------------------------------- |
| `ID`       | Yes      | Gotify application ID                                                                           |
| `Name`     | No       | Human-readable application name (used in Telegram messages; defaults to `""` if omitted)        |
| `Priority` | No       | Minimum priority (0–10) for notifications. Defaults to `0`                                      |


Example `subscriptions.json`
```json
[
  {
    "ID": 1,
    "Name": "App Name 1"
  },
  {
    "ID": 3,
    "Priority": 2
  }
]
```

## How to Get Your Telegram Token and Chat ID

1. **Create a Telegram Bot**  
   Talk to [@BotFather](https://t.me/BotFather) and use the `/newbot` command to create your bot.  
   You’ll receive a **bot token** — save this, it will be your `TELEGRAM_TOKEN`.

2. **Get Your Chat ID**  
   - Start a conversation with your bot (send `/start`)  
   - Visit: `https://api.telegram.org/bot<TELEGRAM_TOKEN>/getUpdates`  
     *(replace `<TELEGRAM_TOKEN>` with your actual token)*  
   - Your `chat id` will appear in the response — that’s your `TELEGRAM_CHAT_ID`.


## Getting Started

### Option 1: Run Locally

```bash
git clone https://github.com/Tiagura/gotigram.git
cd gotigram

go mod tidy

go build -o gotigram main.go

export GOTIFY_WS_URL=ws://<GOTIFY_SERVER>:<WS_PORT>
export GOTIFY_REST_URL=http(s)://<GOTIFY_SERVER>:<REST_PORT>
export GOTIFY_CLIENT_TOKEN=<YOUR_GOTIFY_CLIENT_TOKEN>
export TELEGRAM_TOKEN=<YOUR_TELEGRAM_BOT_TOKEN>
export TELEGRAM_CHAT_ID=<YOUR_TELEGRAM_CHAT_ID>
export SUBSCRIPTIONS_FILE=<path/to/json>  # Optional to set
export TELEGRAM_TEMPLATE=''               # Optional to set

./gotigram
```

### Option 2: Run via Docker

#### Using a local build

[Example file](local-docker-compose.yml)

```bash
git clone https://github.com/Tiagura/gotigram.git
cd gotigram
mkdir subscriptions
export MYUID=$(id -u)
export MYGID=$(id -g)
docker compose -f local-docker-compose.yml up -d
```

#### Using Docker Hub image

[Example file](docker-compose.yml)

```bash
git clone https://github.com/Tiagura/gotigram.git
cd gotigram
mkdir subscriptions
export MYUID=$(id -u)
export MYGID=$(id -g)
docker compose -f docker-compose.yml up -d
```

## Bot Usage

Once the bot is running, open a Telegram chat with it and send the `/start` command to begin. This step is optional but recommended, as it displays all available commands.

### Available Commands

- `/help` - Show help message  
- `/apps` - List all applications on the Gotify server, with subscription status.
- `/subscribe <app_id|all>[,<priority>]` - Subscribe to a specific application by its ID, or to all applications using all. Optionally set a priority (0–10); defaults to 0.
- `/subscriptions` - Show a list of your current subscriptions, including priority.
- `/unsubscribe <app_id|app_id1,app_id2,...|all>` - Unsubscribe from one or more applications (comma-separated IDs) or from all subscriptions using all.
- `/export` - Export the current subscriptions as a JSON array directly in the Telegram chat.
- `/import` - Import subscriptions from a JSON array
- `/save` - Save the current subscriptions to the configured subscriptions file (`SUBSCRIPTIONS_FILE`) for automatic loading on next startup.

### Subscribing to Applications

To start receiving Gotify messages in Telegram, you must subscribe to specific applications. This allows you to filter which messages you want forwarded. To find application IDs, use the command:

```
/apps
```

#### Example

<img src="images/subscribe_example.png" alt="subscribe_example" width="500"/>

## License

This project is open-source and available under the [MIT License](LICENSE).


## Contributing

Feel free to open issues or submit pull requests to improve Gotigram!