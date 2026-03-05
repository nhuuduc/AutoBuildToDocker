package bot

import (
	"fmt"
	"log"

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
				if sender == nil || !allowedSet[sender.ID] {
					id := int64(0)
					if sender != nil {
						id = sender.ID
					}
					log.Printf("[Bot] Unauthorized access attempt from user %d", id)
					return c.Send(fmt.Sprintf("⛔ Unauthorized\\. User ID `%d` is not allowed to use this bot\\.", id), tele.ModeMarkdownV2)
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

	// Initialize notification service with this bot
	services.InitNotifications(bot)

	log.Println("Telegram bot configured")
	return bot, nil
}
