package bot

import (
	"fmt"
	"log"
	"strings"

	"github.com/nhd/autobuildtodocker/internal/bot/handlers"
	"github.com/nhd/autobuildtodocker/internal/config"
	"github.com/nhd/autobuildtodocker/internal/services"
	tele "gopkg.in/telebot.v3"
	"gopkg.in/telebot.v3/middleware"
)

// New creates and configures the Telegram bot.
func New() (*tele.Bot, error) {
	cfg := config.App
	if cfg.Telegram.BotToken == "" {
		return nil, nil // Bot not configured — skip gracefully
	}

	pref := tele.Settings{
		Token:  cfg.Telegram.BotToken,
		Poller: &tele.LongPoller{Timeout: 10},
	}

	bot, err := tele.NewBot(pref)
	if err != nil {
		return nil, err
	}

	// Recovery middleware
	bot.Use(middleware.Recover())

	// ── Whitelist middleware ──────────────────────────────────────────────
	// If ALLOWED_TELEGRAM_IDS is set, only those users can use the bot.
	if len(cfg.Telegram.AllowedUserIDs) > 0 {
		allowedSet := make(map[int64]bool, len(cfg.Telegram.AllowedUserIDs))
		for _, id := range cfg.Telegram.AllowedUserIDs {
			allowedSet[id] = true
		}
		log.Printf("[Bot] Whitelist active: %v", cfg.Telegram.AllowedUserIDs)

		bot.Use(func(next tele.HandlerFunc) tele.HandlerFunc {
			return func(c tele.Context) error {
				sender := c.Sender()
				chat := c.Chat()

				senderAllowed := sender != nil && allowedSet[sender.ID]
				chatAllowed := chat != nil && allowedSet[chat.ID]

				if !senderAllowed && !chatAllowed {
					id := int64(0)
					if sender != nil {
						id = sender.ID
					}
					log.Printf("[Bot] Unauthorized access attempt from user %d in chat %d", id, chat.ID)
					return c.Send(fmt.Sprintf("⛔ Unauthorized. User ID `%d` is not allowed.", id), tele.ModeMarkdown)
				}
				return next(c)
			}
		})
	}

	// Logging middleware
	bot.Use(func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			sender := c.Sender()
			username := "unknown"
			if sender != nil {
				username = sender.Username
			}
			log.Printf("[Bot] Message from %d (@%s)", sender.ID, username)
			return next(c)
		}
	})

	// Register all slash commands and callbacks
	handlers.RegisterCommands(bot)
	handlers.RegisterCallbacks(bot)

	// ── @Mention handler for groups ──────────────────────────────────────
	// Allows "@BotName /command args" format as alternative to "/command@BotName"
	// Requires privacy mode OFF in BotFather for group non-command messages.
	// Works automatically for commands (privacy mode ON) since commands are
	// always delivered; OnText catches the @mention + text messages.
	botUsername := strings.ToLower(bot.Me.Username)
	bot.Handle(tele.OnText, func(c tele.Context) error {
		text := strings.TrimSpace(c.Text())
		prefix := "@" + botUsername + " "
		if !strings.HasPrefix(strings.ToLower(text), strings.ToLower(prefix)) {
			return nil // not addressed to this bot
		}
		rest := strings.TrimSpace(text[len(prefix):])
		if rest == "" {
			return handlers.RouteCommand("/start", "", c)
		}
		// Split into command and args
		parts := strings.SplitN(rest, " ", 2)
		cmd := parts[0]
		args := ""
		if len(parts) > 1 {
			args = parts[1]
		}
		if !strings.HasPrefix(cmd, "/") {
			cmd = "/" + cmd // allow "@bot help" as well as "@bot /help"
		}
		log.Printf("[Bot] @mention command: %s args: %q from %d", cmd, args, c.Sender().ID)
		return handlers.RouteCommand(cmd, args, c)
	})

	// Initialize notification service with this bot
	services.InitNotifications(bot)

	log.Println("Telegram bot configured")
	return bot, nil
}
