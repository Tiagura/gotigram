import os
import asyncio
import json
import logging
import aiohttp
from telegram import Bot, Update
from telegram.ext import Application, CommandHandler, ContextTypes, MessageHandler, filters
from dotenv import load_dotenv

# Load environment variables from .env file
load_dotenv()

def require_env(var_name: str) -> str:
    value = os.environ.get(var_name)
    if not value:
        print(f"Error: {var_name} environment variable not set!")
        sys.exit(1)
    return value

# Required ENV variables
GOTIFY_WS_URL = require_env('GOTIFY_WS_URL')
GOTIFY_REST_URL = require_env('GOTIFY_REST_URL')
GOTIFY_CLIENT_TOKEN = require_env('GOTIFY_CLIENT_TOKEN')
TELEGRAM_TOKEN = require_env('TELEGRAM_TOKEN')
TELEGRAM_CHAT_ID = require_env('TELEGRAM_CHAT_ID')

# Ensure the log directory exists
os.makedirs("logs", exist_ok=True)

# Enable logging
logging.basicConfig(
    filename='logs/bot.log',
    format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
    level=logging.INFO
)

logger = logging.getLogger(__name__)

# In-memory subscriptions (can be replaced with DB later)
subscriptions = set()

HELP_TEXT = """
Available Commands:
/help - Show this help message
/subscribe <app_id> - Subscribe to an app
/unsubscribe <app_id> - Unsubscribe from an app
/subscriptions - Show current subscriptions
/apps - Show all applications
""".strip()

# -------------------------
# Telegram Bot Command Logic
# -------------------------

async def start(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    """Start the conversation, when the command /start is issued."""
    reply_text = "Hi! I'm Gotigram. Why don't you subscribe to application to receive their notifications?\nUse /help to see the commands available"
    logger.info(f"New conversation started")
    await update.message.reply_text(reply_text)


async def help_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    """Send a message when the command /help is issued."""
    await update.message.reply_text(HELP_TEXT)

async def subscribe_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    """Add app id to subscriptions and send corresponding message when the command /subscribe <id> is issued."""
    if not context.args:
        logger.warn(f"Failed subscribe command.")
        await update.message.reply_text(" Usage: /subscribe <app_id>")
        return

    app_input = " ".join(context.args).strip()
    apps = await fetch_gotify_channels()

    if not apps:
        logger.info(f"No available apps found.")
        await update.message.reply_text("No available apps found.")
        return

    app_id = None

    if app_input.isdigit():
        app_id = int(app_input)
        if app_id not in [id_ for id_, _ in apps]:
            logger.warn(f"No application with id {app_id} found.")
            await update.message.reply_text(f"No application found with ID {app_id}.")
            return
    else:
        logger.error(f"Application ID must be a number.")
        await update.message.reply_text("App id must be a number")
        return

    if app_id in subscriptions:
        logger.warn(f"Already subscribed to application ID {app_id}")
        await update.message.reply_text(f"Already subscribed to application ID {app_id}.")
    else:
        subscriptions.add(app_id)
        logger.info(f"Subscribed to application ID {app_id}.")
        await update.message.reply_text(f"Subscribed to application ID {app_id}.")

async def unsubscribe_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    """Remove app ID from subscriptions and send corresponding message when the command /unsubscribe <id> is issued."""
    if not context.args:
        logger.warning("Failed unsubscribe command.")
        await update.message.reply_text("Usage: /unsubscribe <app_id>")
        return

    app_input = " ".join(context.args).strip()

    if not app_input.isdigit():
        logger.error("Application ID must be a number.")
        await update.message.reply_text("App ID must be a number.")
        return

    app_id = int(app_input)

    if app_id not in subscriptions:
        logger.warning(f"Not subscribed to application ID {app_id}.")
        await update.message.reply_text(f"You are not subscribed to application ID {app_id}.")
    else:
        subscriptions.remove(app_id)
        logger.info(f"Unsubscribed from application ID {app_id}.")
        await update.message.reply_text(f"Unsubscribed from application ID {app_id}.")

async def subscriptions_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    """List current subscriptions in the format 'app_id: app_name', when command /subscriptions is issued."""
    if not subscriptions:
        logger.info(f"User not subscribed to any applications.")
        await update.message.reply_text("You are not subscribed to any applications.")
        return

    apps = await fetch_gotify_channels()
    if not apps:
        logger.info(f"No available apps found.")
        await update.message.reply_text("Unable to fetch application list.")
        return

    # Build a mapping of id -> name
    app_dict = dict(apps)

    subscribed_list = [
        f"{app_id}: {app_dict.get(app_id, 'Unknown')}" for app_id in subscriptions
    ]
    reply = "Current subscriptions:\n" + "\n".join(subscribed_list)
    await update.message.reply_text(reply)

async def apps_command(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    """List all Gotify apps with subscription status, when command /apps is issued)."""
    apps = await fetch_gotify_channels()

    if not apps:
        logger.info(f"No available apps found.")
        await update.message.reply_text("No available applications found.")
        return

    lines = []
    for app_id, app_name in apps:
        status = "✅" if app_id in subscriptions else "❌"
        lines.append(f"{app_id}: {app_name} -> {status}")

    reply = "Available applications:\n" + "\n".join(lines)
    await update.message.reply_text(reply)


# -------------------------
# Gotify REST API Methods
# -------------------------
async def fetch_gotify_channels():
    gotify_apps_url = f"{GOTIFY_REST_URL}/application"
    headers = {"X-Gotify-Key": GOTIFY_CLIENT_TOKEN}

    async with aiohttp.ClientSession() as session:
        async with session.get(gotify_apps_url, headers=headers) as response:
            if response.status == 200:
                apps = await response.json()
                return [(app["id"], app["name"]) for app in apps]
            else:
                logger.error(f"Failed to fetch channels: {response.status}")
                return []

# -------------------------
# Gotify WebSocket Listener
# -------------------------
async def listen_gotify_stream(bot: Bot):
    stream_url = f"{GOTIFY_WS_URL}/stream?token={GOTIFY_CLIENT_TOKEN}"

    async with aiohttp.ClientSession() as session:
        async with session.ws_connect(stream_url) as ws:
           logger.info("Connected to Gotify stream")

           async for msg in ws:
                if msg.type == aiohttp.WSMsgType.TEXT:
                    try:
                        payload = json.loads(msg.data)
                        title = payload.get("title", "No Title")
                        message = payload.get("message", "")
                        app = payload.get("appid", "unknown")
                        logger.info(f"Gotify message from app {app}: {title} - {message}")
                        # Filter messages by subscribed apps
                        if app in subscriptions:
                            logger.info(f"Message from subscribed application ID {app}")
                            await bot.send_message(chat_id=TELEGRAM_CHAT_ID, text=f"{title} - {message}", parse_mode='Markdown')
                        else:
                            logger.info(f"Message from unsubscribed application ID {app}, ignoring.")

                    except Exception as e:
                        logger.error(f"⚠️ Error parsing message: {e}")
                elif msg.type == aiohttp.WSMsgType.ERROR:
                    logger.error("🚨 WebSocket connection error!")
                    break

async def main():
    application = Application.builder().token(TELEGRAM_TOKEN).build()

    application.add_handler(CommandHandler("start", start))
    application.add_handler(CommandHandler("help", help_command))
    application.add_handler(CommandHandler("subscribe", subscribe_command))
    application.add_handler(CommandHandler("unsubscribe", unsubscribe_command))
    application.add_handler(CommandHandler("subscriptions",subscriptions_command))
    application.add_handler(CommandHandler("apps",apps_command))

    # Start the bot without blocking
    await application.initialize()
    await application.start()
    # Start polling in the background
    await application.updater.start_polling()

    # Run the websocket listener concurrently
    await listen_gotify_stream(application.bot)

    # When websocket listener ends, stop the bot gracefully
    await application.updater.stop()
    await application.stop()
    await application.shutdown()

if __name__ == '__main__':
    asyncio.run(main())
