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

	log.Printf("[Callbacks] Routing on: %q", data) // show exact bytes after stripping

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
		log.Printf("[Callbacks] No matching handler for: %q", data)
		return c.Respond(&tele.CallbackResponse{Text: "Unknown action"})
	}
}

// ── build: ────────────────────────────────────────────────────────────────────

// handleBuildCallback — format: build:owner/repo:commitSHA (from scheduler notification)
// Shows a mode selection menu (Local / GitHub Actions) before queuing.
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

	sender := c.Sender()
	if sender == nil {
		return c.Respond(&tele.CallbackResponse{Text: "Cannot identify user"})
	}

	dbUser, _ := db.FindUserByTelegramID(sender.ID)
	if dbUser == nil {
		return c.Respond(&tele.CallbackResponse{Text: "User not found. Please /start first."})
	}

	owner, repo := split[0], split[1]
	repoData, _ := db.FindRepoByUserAndFullName(dbUser.ID, owner, repo)
	if repoData == nil {
		return c.Respond(&tele.CallbackResponse{Text: "Repository not found"})
	}
	_ = db.DeleteConfirmationsByRepo(repoData.ID)

	// Truncate SHA to 12 chars for button data (64 byte Telegram limit)
	sha12 := commitSHA
	if len(sha12) > 12 {
		sha12 = sha12[:12]
	}

	// Show mode selection: Local vs GitHub Actions
	btnLocal := tele.InlineButton{
		Text:   "🖥️ Local Server",
		Unique: "mode_local",
		Data:   fmt.Sprintf("mode:local:%s:%s", repoFullName, sha12),
	}
	btnActions := tele.InlineButton{
		Text:   "🚀 GitHub Actions",
		Unique: "mode_actions",
		Data:   fmt.Sprintf("mode:actions:%s:%s", repoFullName, sha12),
	}
	kb := &tele.ReplyMarkup{}
	kb.InlineKeyboard = [][]tele.InlineButton{{btnLocal, btnActions}}

	editText := fmt.Sprintf(
		"🐳 *Build:* `%s`\n🔗 Commit: `%s`\n\nChọn nơi build:",
		repoFullName, sha12[:min(7, len(sha12))],
	)
	if editErr := c.Edit(editText, tele.ModeMarkdown, kb); editErr != nil {
		log.Printf("[Build] Edit failed: %v — sending new message", editErr)
		_ = c.Send(editText, tele.ModeMarkdown, kb)
	}
	return c.Respond(&tele.CallbackResponse{Text: "Chọn nơi build"})
}

// ── mode: ─────────────────────────────────────────────────────────────────────

// handleModeCallback — format: mode:local:owner/repo:sha12 or mode:actions:owner/repo:sha12
func handleModeCallback(c tele.Context, data string) error {
	log.Printf("[Mode] Parsing data: %q", data)
	parts := strings.SplitN(data, ":", 4)
	if len(parts) < 4 {
		log.Printf("[Mode] Invalid parts count: %d", len(parts))
		return c.Respond(&tele.CallbackResponse{Text: "Invalid mode request"})
	}
	buildMode := parts[1]    // "local" or "actions"
	repoFullName := parts[2] // "owner/repo"
	sha12 := parts[3]
	log.Printf("[Mode] buildMode=%s repo=%s sha=%s", buildMode, repoFullName, sha12)

	split := strings.SplitN(repoFullName, "/", 2)
	if len(split) < 2 {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid repo format"})
	}
	owner, repo := split[0], split[1]

	sender := c.Sender()
	if sender == nil {
		return c.Respond(&tele.CallbackResponse{Text: "Cannot identify user"})
	}
	log.Printf("[Mode] sender ID=%d", sender.ID)

	dbUser, err := db.FindUserByTelegramID(sender.ID)
	if err != nil {
		log.Printf("[Mode] DB error finding user %d: %v", sender.ID, err)
	}
	if dbUser == nil {
		log.Printf("[Mode] User not found for TelegramID=%d", sender.ID)
		return c.Respond(&tele.CallbackResponse{Text: "User not found. Please /start first."})
	}

	repoData, err := db.FindRepoByUserAndFullName(dbUser.ID, owner, repo)
	if err != nil {
		log.Printf("[Mode] DB error finding repo %s: %v", repoFullName, err)
	}
	if repoData == nil {
		log.Printf("[Mode] Repo not found: %s for userID=%d", repoFullName, dbUser.ID)
		return c.Respond(&tele.CallbackResponse{Text: "Repository not found"})
	}
	log.Printf("[Mode] Found repo ID=%d", repoData.ID)

	// Resolve short SHA → full 40-char SHA
	fullSHA, err := services.ResolveCommitSHA(owner, repo, sha12)
	if err != nil {
		log.Printf("[Mode] Could not resolve SHA %s: %v — using partial", sha12, err)
		fullSHA = sha12
	}

	// Both modes: store pending state and show feature selection menu
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

	modeEmoji := "🖥️"
	modeLabel := "Local Build"
	if buildMode == "actions" {
		modeEmoji = "🚀"
		modeLabel = "GitHub Actions Build"
	}
	editText := fmt.Sprintf(
		"%s *%s:* `%s`\n🔗 Commit: `%s`\n\n🛠️ *Select optional features to install:*",
		modeEmoji, modeLabel, repoFullName, sha12[:min(7, len(sha12))],
	)
	kb := BuildFeatureKeyboard(pb)
	if editErr := c.Edit(editText, tele.ModeMarkdown, kb); editErr != nil {
		log.Printf("[Mode] Edit failed: %v — sending new message", editErr)
		if _, sendErr := c.Bot().Send(c.Chat(), editText, tele.ModeMarkdown, kb); sendErr != nil {
			log.Printf("[Mode] Send also failed: %v", sendErr)
		}
	}
	log.Printf("[Mode] Feature menu shown for %s @ %s mode=%s", repoFullName, sha12, buildMode)
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
		modeEmoji2 := "🖥️"
		modeLabel2 := "Local"
		if pb.BuildMode == "actions" {
			modeEmoji2 = "🚀"
			modeLabel2 = "GitHub Actions"
		}
		editText := fmt.Sprintf(
			"%s *%s Build:* `%s`\n🔗 Commit: `%s`\n\n🛠️ *Select optional features:*",
			modeEmoji2, modeLabel2, repoFull, sha12[:min(7, len(sha12))],
		)
		_ = c.Edit(editText, tele.ModeMarkdown, kb)
		return c.Respond(&tele.CallbackResponse{Text: label + " " + status})

	case "build":
		// New format: rest = "mode:owner/repo:sha12"
		// e.g. "actions:RightNow-AI/openfang:ebcdc17c138e"
		buildModeAndRest := strings.SplitN(rest, ":", 2)
		if len(buildModeAndRest) < 2 {
			return c.Respond(&tele.CallbackResponse{Text: "Invalid build data"})
		}
		buildMode := buildModeAndRest[0]  // "local" or "actions"
		repoAndSha := buildModeAndRest[1] // "owner/repo:sha12"

		colonIdx := strings.LastIndex(repoAndSha, ":")
		if colonIdx < 0 {
			return c.Respond(&tele.CallbackResponse{Text: "Invalid build data"})
		}
		repoFull := repoAndSha[:colonIdx]
		sha12 := repoAndSha[colonIdx+1:]

		// Try to get pending state (features selected by user)
		pb := GetPending(repoFull, sha12)

		var selectedFeatures []string
		var repoID int64
		var fullSHA, imageName string

		if pb != nil {
			// Happy path: pending state exists with selected features
			for _, f := range services.AvailableFeatures {
				if pb.Features[f.Key] {
					selectedFeatures = append(selectedFeatures, f.Key)
				}
			}
			repoID = pb.RepoID
			fullSHA = pb.FullSHA
			imageName = pb.ImageName
			DeletePending(repoFull, sha12)
		} else {
			// Fallback: server restarted, look up from DB
			log.Printf("[Feat] Pending state lost for %s @ %s — doing DB lookup", repoFull, sha12)
			sender := c.Sender()
			if sender == nil {
				return c.Respond(&tele.CallbackResponse{Text: "Cannot identify user"})
			}
			dbUser, _ := db.FindUserByTelegramID(sender.ID)
			if dbUser == nil {
				return c.Respond(&tele.CallbackResponse{Text: "Session expired. Please /build again."})
			}
			repoParts := strings.SplitN(repoFull, "/", 2)
			if len(repoParts) < 2 {
				return c.Respond(&tele.CallbackResponse{Text: "Invalid repo"})
			}
			repoData, _ := db.FindRepoByUserAndFullName(dbUser.ID, repoParts[0], repoParts[1])
			if repoData == nil {
				return c.Respond(&tele.CallbackResponse{Text: "Repository not found"})
			}
			repoID = repoData.ID
			imageName = repoData.ImageName
			fullSHA = sha12 // best we can do without pending state
			// No features selected (state lost)
		}

		services.AddToQueueWithFeatures(repoID, repoFull, fullSHA, imageName, buildMode, selectedFeatures)

		featDesc := "none"
		if len(selectedFeatures) > 0 {
			featDesc = strings.Join(selectedFeatures, ", ")
		}
		buildEmoji := "🖥️"
		buildLabel := "Local Build"
		if buildMode == "actions" {
			buildEmoji = "🚀"
			buildLabel = "GitHub Actions Build"
		}
		editText := fmt.Sprintf(
			"%s *%s Queued!*\n\n📦 Repository: `%s`\n🔗 Commit: `%s`\n🛠️ Features: `%s`",
			buildEmoji, buildLabel, repoFull, sha12[:min(7, len(sha12))], featDesc,
		)
		if editErr := c.Edit(editText, tele.ModeMarkdown); editErr != nil {
			log.Printf("[Feat] Edit failed on build confirm: %v", editErr)
			_ = c.Send(editText, tele.ModeMarkdown)
		}
		log.Printf("[Feat] %s build queued for %s @ %s features=%v", buildMode, repoFull, sha12, selectedFeatures)
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
