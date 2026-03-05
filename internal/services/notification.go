package services

import (
	"fmt"
	"log"
	"sync"

	tele "gopkg.in/telebot.v3"
)

// UpdateNotification holds info for commit/release notification.
type UpdateNotification struct {
	Type      string // "commit" or "release"
	Repo      string
	Branch    string
	SHA       string
	Tag       string
	ImageName string
}

// BuildStatus holds build progress info.
type BuildStatus struct {
	Repo      string
	Status    string // "pending", "running", "success", "failed"
	ImageName string
	Message   string
}

var (
	notifMu sync.RWMutex
	botInst *tele.Bot
)

// InitNotifications sets the bot instance for sending messages.
func InitNotifications(bot *tele.Bot) {
	notifMu.Lock()
	defer notifMu.Unlock()
	botInst = bot
}

func getBot() *tele.Bot {
	notifMu.RLock()
	defer notifMu.RUnlock()
	return botInst
}

// NotifyUser sends a commit or release notification with Build/Skip buttons.
func NotifyUser(telegramID int64, update UpdateNotification) error {
	bot := getBot()
	if bot == nil {
		log.Println("[Notification] Bot not initialized")
		return nil
	}

	var text string
	var markup *tele.ReplyMarkup

	chat := &tele.Chat{ID: telegramID}

	if update.Type == "commit" {
		text = fmt.Sprintf(
			"🔔 *New Commit Detected*\n\n"+
				"📦 *Repository:* `%s`\n"+
				"🌿 *Branch:* `%s`\n"+
				"🔗 *SHA:* `%s`\n"+
				"🐳 *Image:* `%s`",
			update.Repo, update.Branch, update.SHA[:7], update.ImageName,
		)
	} else {
		text = fmt.Sprintf(
			"🚀 *New Release Detected*\n\n"+
				"📦 *Repository:* `%s`\n"+
				"🏷️ *Tag:* `%s`\n"+
				"🔗 *SHA:* `%s`\n"+
				"🐳 *Image:* `%s`",
			update.Repo, update.Tag, update.SHA[:7], update.ImageName,
		)
	}

	// Truncate SHA to 12 chars — Telegram callback data limit is 64 bytes.
	// build:<repo>:<sha12> e.g. "build:zeroclaw-labs/zeroclaw:4705a74abc12" = 44 bytes max
	sha12 := update.SHA
	if len(sha12) > 12 {
		sha12 = sha12[:12]
	}
	markup = &tele.ReplyMarkup{}
	buildBtn := markup.Data("🔨 Build Now", "build_trigger",
		fmt.Sprintf("build:%s:%s", update.Repo, sha12))
	skipBtn := markup.Data("Skip", "skip_trigger",
		fmt.Sprintf("skip:%s", update.Repo))
	markup.Inline(markup.Row(buildBtn, skipBtn))

	_, err := bot.Send(chat, text, &tele.SendOptions{
		ParseMode:   tele.ModeMarkdown,
		ReplyMarkup: markup,
	})
	if err != nil {
		log.Printf("[Notification] Failed to send to %d: %v", telegramID, err)
	} else {
		log.Printf("[Notification] Sent %s notification to %d", update.Type, telegramID)
	}
	return err
}

// SendBuildStatus sends a build status update message.
func SendBuildStatus(telegramID int64, status BuildStatus) error {
	bot := getBot()
	if bot == nil {
		return nil
	}

	emoji := map[string]string{
		"pending": "⏳",
		"running": "⚙️",
		"success": "✅",
		"failed":  "❌",
	}
	statusText := map[string]string{
		"pending": "Queued",
		"running": "Building...",
		"success": "Build Successful",
		"failed":  "Build Failed",
	}

	text := fmt.Sprintf("%s *Build %s*\n\n📦 *Repository:* `%s`",
		emoji[status.Status], statusText[status.Status], status.Repo)

	if status.ImageName != "" {
		text += fmt.Sprintf("\n🐳 *Image:* `%s`", status.ImageName)
	}
	if status.Message != "" {
		text += fmt.Sprintf("\n📝 *Message:* %s", status.Message)
	}

	chat := &tele.Chat{ID: telegramID}
	_, err := bot.Send(chat, text, tele.ModeMarkdown)
	if err != nil {
		log.Printf("[Notification] Failed to send build status to %d: %v", telegramID, err)
	}
	return err
}

// SendMessage sends a plain text message to a user.
func SendMessage(telegramID int64, text string) error {
	bot := getBot()
	if bot == nil {
		return nil
	}
	chat := &tele.Chat{ID: telegramID}
	_, err := bot.Send(chat, text, tele.ModeMarkdown)
	return err
}
