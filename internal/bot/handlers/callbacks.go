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

	// When using InlineButton.Unique, telebot prepends "{unique}|" before the actual data.
	// e.g. "mode_local|mode:local:owner/repo:sha" → strip to "mode:local:owner/repo:sha"
	if idx := strings.Index(data, "|"); idx != -1 {
		data = data[idx+1:]
	}

	switch {
	case strings.HasPrefix(data, "build:"):
		return handleBuildCallback(c, data)
	case strings.HasPrefix(data, "skip:"):
		return handleSkipCallback(c, data)
	case strings.HasPrefix(data, "mode:"):
		return handleModeCallback(c, data)
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

	services.AddToQueue(repoData.ID, repoFullName, commitSHA, repoData.ImageName, "actions")
	_ = db.DeleteConfirmationsByRepo(repoData.ID)

	editText := fmt.Sprintf("✅ *Build Queued*\n\n📦 Repository: `%s`\n🔗 Commit: `%s`",
		repoFullName, commitSHA[:7])
	_ = c.Edit(editText, tele.ModeMarkdownV2)

	return c.Respond(&tele.CallbackResponse{Text: "Build queued!"})
}

// handleModeCallback — format: mode:local:owner/repo:commitSHA
//
//	stores build mode choice and queues the job.
func handleModeCallback(c tele.Context, data string) error {
	// data = "mode:local:owner/repo:commitSHA" or "mode:actions:owner/repo:..."
	parts := strings.SplitN(data, ":", 4)
	if len(parts) < 4 {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid mode request"})
	}
	buildMode := parts[1]    // "local" or "actions"
	repoFullName := parts[2] // "owner/repo"
	commitSHA := parts[3]

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

	// Resolve partial SHA (12 chars from button data) to full 40-char SHA.
	// Git and GitHub Actions both need the full SHA for reliable fetches.
	fullSHA, err := services.ResolveCommitSHA(owner, repo, commitSHA)
	if err != nil {
		log.Printf("[Callbacks] Could not resolve SHA %s for %s: %v — using partial", commitSHA, repoFullName, err)
		fullSHA = commitSHA // fallback: use what we have
	}

	services.AddToQueue(repoData.ID, repoFullName, fullSHA, repoData.ImageName, buildMode)

	modeLabel := "🚀 GitHub Actions"
	if buildMode == "local" {
		modeLabel = "🖥️ Local Server"
	}

	shortSHA := commitSHA
	if len(commitSHA) > 7 {
		shortSHA = commitSHA[:7]
	}

	editText := fmt.Sprintf("✅ *Build Queued* \u00b7 %s\n\n📦 Repository: `%s`\n🔗 Commit: `%s`",
		modeLabel, repoFullName, shortSHA)
	_ = c.Edit(editText, tele.ModeMarkdown)

	log.Printf("[Callbacks] Mode selected: %s for %s @ %s by user %d", buildMode, repoFullName, shortSHA, sender.ID)
	return c.Respond(&tele.CallbackResponse{Text: "✅ Build queued!"})
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
