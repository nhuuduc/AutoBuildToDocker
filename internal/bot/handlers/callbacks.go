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
	case strings.HasPrefix(data, "feat:"):
		return handleFeatCallback(c, data)
	default:
		return c.Respond(&tele.CallbackResponse{Text: "Unknown action"})
	}
}

// ── build: ────────────────────────────────────────────────────────────────────

// handleBuildCallback — format: build:owner/repo:commitSHA (from scheduler notification)
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
		repoFullName, commitSHA)
	_ = c.Edit(editText, tele.ModeMarkdown)

	return c.Respond(&tele.CallbackResponse{Text: "Build queued!"})
}

// ── mode: ─────────────────────────────────────────────────────────────────────

// handleModeCallback — format: mode:local:owner/repo:sha12 or mode:actions:owner/repo:sha12
func handleModeCallback(c tele.Context, data string) error {
	parts := strings.SplitN(data, ":", 4)
	if len(parts) < 4 {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid mode request"})
	}
	buildMode := parts[1]    // "local" or "actions"
	repoFullName := parts[2] // "owner/repo"
	sha12 := parts[3]

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

	// Resolve short SHA → full 40-char SHA
	fullSHA, err := services.ResolveCommitSHA(owner, repo, sha12)
	if err != nil {
		log.Printf("[Callbacks] Could not resolve SHA %s for %s: %v — using partial", sha12, repoFullName, err)
		fullSHA = sha12
	}

	modeEmoji := "🖥️"
	if buildMode == "actions" {
		modeEmoji = "🚀"
	}

	// Both modes show feature selection menu
	pb := &PendingBuild{
		RepoID:       repoData.ID,
		RepoFullName: repoFullName,
		FullSHA:      fullSHA,
		SHA12:        sha12,
		ImageName:    repoData.ImageName,
		BuildMode:    buildMode,
		Features:     map[string]bool{},
	}
	StorePending(pb)

	modeLabel := "Local Server"
	if buildMode == "actions" {
		modeLabel = "GitHub Actions"
	}
	editText := fmt.Sprintf(
		"%s *%s Build:* `%s`\n🔗 Commit: `%s`\n\n🛠️ *Select optional features:*",
		modeEmoji, modeLabel, repoFullName, sha12[:min(7, len(sha12))],
	)
	kb := BuildFeatureKeyboard(pb)
	_ = c.Edit(editText, tele.ModeMarkdown, kb)
	return c.Respond(&tele.CallbackResponse{Text: "Choose features →"})
}

// ── feat: ─────────────────────────────────────────────────────────────────────

// handleFeatCallback — format: feat:toggle:owner/repo:sha12:featureKey or feat:build:owner/repo:sha12
func handleFeatCallback(c tele.Context, data string) error {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) < 3 {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid feature request"})
	}
	action := parts[1] // "toggle" or "build"
	rest := parts[2]   // "owner/repo:sha12[:featureKey]"

	switch action {
	case "toggle":
		// rest = "owner/repo:sha12:featureKey"
		lastColon := strings.LastIndex(rest, ":")
		if lastColon < 0 {
			return c.Respond(&tele.CallbackResponse{Text: "Invalid toggle data"})
		}
		pbKey := rest[:lastColon] // "owner/repo:sha12"
		featureKey := rest[lastColon+1:]

		// pbKey format = "owner/repo:sha12"
		colonIdx := strings.LastIndex(pbKey, ":")
		repoFull := pbKey[:colonIdx]
		sha12 := pbKey[colonIdx+1:]

		pb := GetPending(repoFull, sha12)
		if pb == nil {
			return c.Respond(&tele.CallbackResponse{Text: "Session expired. Please /build again."})
		}

		// Toggle feature
		pb.Features[featureKey] = !pb.Features[featureKey]
		label := featureKey
		if f := services.FeatureByKey(featureKey); f != nil {
			label = f.Label
		}
		status := "off"
		if pb.Features[featureKey] {
			status = "on"
		}

		kb := BuildFeatureKeyboard(pb)
		editText := fmt.Sprintf(
			"🖥️ *Local Build:* `%s`\n🔗 Commit: `%s`\n\n🛠️ *Select optional features:*",
			repoFull, sha12[:min(7, len(sha12))],
		)
		_ = c.Edit(editText, tele.ModeMarkdown, kb)
		return c.Respond(&tele.CallbackResponse{Text: label + " " + status})

	case "build":
		// rest = "owner/repo:sha12"
		colonIdx := strings.LastIndex(rest, ":")
		if colonIdx < 0 {
			return c.Respond(&tele.CallbackResponse{Text: "Invalid build data"})
		}
		repoFull := rest[:colonIdx]
		sha12 := rest[colonIdx+1:]

		pb := GetPending(repoFull, sha12)
		if pb == nil {
			return c.Respond(&tele.CallbackResponse{Text: "Session expired. Please /build again."})
		}

		// Collect selected features in order
		var selectedFeatures []string
		for _, f := range services.AvailableFeatures {
			if pb.Features[f.Key] {
				selectedFeatures = append(selectedFeatures, f.Key)
			}
		}

		services.AddToQueueWithFeatures(pb.RepoID, pb.RepoFullName, pb.FullSHA, pb.ImageName, pb.BuildMode, selectedFeatures)
		DeletePending(repoFull, sha12)

		featDesc := "none"
		if len(selectedFeatures) > 0 {
			featDesc = strings.Join(selectedFeatures, ", ")
		}
		editText := fmt.Sprintf(
			"🖥️ *Local Build Queued!*\n\n📦 Repository: `%s`\n🔗 Commit: `%s`\n🛠️ Features: `%s`",
			repoFull, sha12[:min(7, len(sha12))], featDesc,
		)
		_ = c.Edit(editText, tele.ModeMarkdown)

		log.Printf("[Callbacks] Local build queued for %s @ %s features=%v", repoFull, sha12, selectedFeatures)
		return c.Respond(&tele.CallbackResponse{Text: "✅ Build queued!"})
	}

	return c.Respond(&tele.CallbackResponse{Text: "Unknown feat action"})
}

// ── skip: ─────────────────────────────────────────────────────────────────────

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
	_ = c.Edit(editText, tele.ModeMarkdown)

	return c.Respond(&tele.CallbackResponse{Text: "Update skipped"})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
