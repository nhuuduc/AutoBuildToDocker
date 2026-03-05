package handlers

import (
	"fmt"
	"log"
	"strings"

	"github.com/nhd/autobuildtodocker/internal/db"
	"github.com/nhd/autobuildtodocker/internal/services"
	tele "gopkg.in/telebot.v3"
)

// RegisterCallbacks registers inline keyboard button handlers.
func RegisterCallbacks(bot *tele.Bot) {
	bot.Handle(tele.OnCallback, handleCallback)
}

func handleCallback(c tele.Context) error {
	data := c.Callback().Data
	if data == "" {
		return c.Respond(&tele.CallbackResponse{Text: "No data"})
	}

	log.Printf("[Callbacks] Received: %s", data)

	// Strip leading \f that telebot adds
	data = strings.TrimPrefix(data, "\f")

	switch {
	case strings.HasPrefix(data, "build:"):
		return handleBuildCallback(c, data)
	case strings.HasPrefix(data, "skip:"):
		return handleSkipCallback(c, data)
	default:
		return c.Respond(&tele.CallbackResponse{Text: "Unknown action"})
	}
}

// handleBuildCallback — format: build:owner/repo:commitSHA
func handleBuildCallback(c tele.Context, data string) error {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) < 3 {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid build request"})
	}
	repoFullName := parts[1]
	commitSHA := parts[2]
	split := strings.SplitN(repoFullName, "/", 2)
	if len(split) < 2 {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid repo format"})
	}
	owner, repo := split[0], split[1]

	sender := c.Sender()
	if sender == nil {
		return c.Respond(&tele.CallbackResponse{Text: "Cannot identify user"})
	}

	dbUser, _ := db.FindUserByTelegramID(sender.ID)
	if dbUser == nil {
		return c.Respond(&tele.CallbackResponse{Text: "User not found. Please /start first."})
	}

	repoData, _ := db.FindRepoByUserAndFullName(dbUser.ID, owner, repo)
	if repoData == nil {
		return c.Respond(&tele.CallbackResponse{Text: "Repository not found"})
	}

	services.AddToQueue(repoData.ID, repoFullName, commitSHA, repoData.ImageName)
	_ = db.DeleteConfirmationsByRepo(repoData.ID)

	// Edit original message
	editText := fmt.Sprintf("✅ *Build Queued*\n\n📦 Repository: `%s`\n🔗 Commit: `%s`",
		repoFullName, commitSHA[:7])
	_ = c.Edit(editText, tele.ModeMarkdownV2)

	return c.Respond(&tele.CallbackResponse{Text: "Build queued!"})
}

// handleSkipCallback — format: skip:owner/repo
func handleSkipCallback(c tele.Context, data string) error {
	parts := strings.SplitN(data, ":", 2)
	if len(parts) < 2 {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid skip request"})
	}
	repoFullName := parts[1]
	split := strings.SplitN(repoFullName, "/", 2)
	if len(split) < 2 {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid repo format"})
	}
	owner, repo := split[0], split[1]

	sender := c.Sender()
	if sender == nil {
		return c.Respond(&tele.CallbackResponse{Text: "Cannot identify user"})
	}

	dbUser, _ := db.FindUserByTelegramID(sender.ID)
	if dbUser == nil {
		return c.Respond(&tele.CallbackResponse{Text: "User not found"})
	}

	repoData, _ := db.FindRepoByUserAndFullName(dbUser.ID, owner, repo)
	if repoData != nil {
		_ = db.DeleteConfirmationsByRepo(repoData.ID)
	}

	editText := fmt.Sprintf("⏭️ *Update Skipped*\n\n📦 Repository: `%s`", repoFullName)
	_ = c.Edit(editText, tele.ModeMarkdownV2)

	return c.Respond(&tele.CallbackResponse{Text: "Update skipped"})
}
