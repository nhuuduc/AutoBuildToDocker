package services

import (
	"fmt"
	"log"
	"sync"

	tele "gopkg.in/telebot.v3"
)

// shortSHA safely returns up to n chars of a SHA/ref string (avoids panic when SHA is a branch name).
func shortSHA(sha string, n int) string {
	if len(sha) <= n {
		return sha
	}
	return sha[:n]
}

// UpdateNotification holds info for commit/release notification.
type UpdateNotification struct {
	Type      string // "commit" or "release"
	RepoID    int64  // DB repo ID — used in button data to avoid long repo names
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
			update.Repo, update.Branch, shortSHA(update.SHA, 7), update.ImageName,
		)
	} else {
		text = fmt.Sprintf(
			"🚀 *New Release Detected*\n\n"+
				"📦 *Repository:* `%s`\n"+
				"🏷️ *Tag:* `%s`\n"+
				"🔗 *SHA:* `%s`\n"+
				"🐳 *Image:* `%s`",
			update.Repo, update.Tag, shortSHA(update.SHA, 7), update.ImageName,
		)
	}

	// Use raw InlineButton (no Unique field) to avoid telebot prepending
	// \f{Unique}| to callback data — would exceed 64-byte limit for long repo names.
	// Use RepoID instead of repo name: "build:{repoID}:{sha8}" → max ~25 bytes.
	sha8 := shortSHA(update.SHA, 8)
	markup = &tele.ReplyMarkup{}
	markup.InlineKeyboard = [][]tele.InlineButton{
		{
			{Text: "🔨 Build Now", Data: fmt.Sprintf("build:%d:%s", update.RepoID, sha8)},
			{Text: "Skip", Data: fmt.Sprintf("skip:%d", update.RepoID)},
		},
	}

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
