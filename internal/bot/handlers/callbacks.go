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

	log.Printf("[Callbacks] Routing on: %q", data)

	switch {
	case strings.HasPrefix(data, "build:"):
		return handleBuildCallback(c, data)
	case strings.HasPrefix(data, "skip:"):
		return handleSkipCallback(c, data)
	case strings.HasPrefix(data, "mode:"):
		return handleModeCallback(c, data)
	case strings.HasPrefix(data, "plat:"):
		return handlePlatCallback(c, data)
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
	// Respond immediately for snappy UX — Telegram shows loading until we do this.
	_ = c.Respond(&tele.CallbackResponse{})

	parts := strings.SplitN(data, ":", 3)
	if len(parts) < 3 {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid build request"})
	}
	repoFullName := parts[1]
	commitSHA := parts[2]
	split := strings.SplitN(repoFullName, "/", 2)
	if len(split) < 2 {
		return nil
	}

	sender := c.Sender()
	if sender == nil {
		return nil
	}

	dbUser, _ := db.FindUserByTelegramID(sender.ID)
	if dbUser == nil {
		return nil
	}

	owner, repo := split[0], split[1]
	repoData, _ := db.FindRepoByUserAndFullName(dbUser.ID, owner, repo)
	if repoData == nil {
		return nil
	}
	_ = db.DeleteConfirmationsByRepo(repoData.ID)

	// Truncate SHA to 12 chars for button data (64 byte Telegram limit)
	sha12 := commitSHA
	if len(sha12) > 12 {
		sha12 = sha12[:12]
	}

	// Show mode selection: Local vs GitHub Actions
	// NOTE: No Unique field — telebot prepends \f{Unique}| to Data, which can
	// push past Telegram's 64-byte callback_data limit for longer repo names.
	// Our generic OnCallback handler routes by Data prefix ("mode:") instead.
	btnLocal := tele.InlineButton{
		Text: "🖥️ Local Server",
		Data: fmt.Sprintf("mode:local:%s:%s", repoFullName, sha12),
	}
	btnActions := tele.InlineButton{
		Text: "🚀 GitHub Actions",
		Data: fmt.Sprintf("mode:actions:%s:%s", repoFullName, sha12),
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
	return nil
}

// ── mode: ─────────────────────────────────────────────────────────────────────

// handleModeCallback — format: mode:local:owner/repo:sha12 or mode:actions:owner/repo:sha12
// Now shows platform selection (amd64/arm64/both) instead of jumping straight to features.
func handleModeCallback(c tele.Context, data string) error {
	// Respond immediately — heavy work (DB + GitHub API) follows.
	_ = c.Respond(&tele.CallbackResponse{})

	log.Printf("[Mode] Parsing data: %q", data)
	parts := strings.SplitN(data, ":", 4)
	if len(parts) < 4 {
		log.Printf("[Mode] Invalid parts count: %d", len(parts))
		return nil
	}
	buildMode := parts[1]    // "local" or "actions"
	repoFullName := parts[2] // "owner/repo"
	sha12 := parts[3]
	log.Printf("[Mode] buildMode=%s repo=%s sha=%s", buildMode, repoFullName, sha12)

	split := strings.SplitN(repoFullName, "/", 2)
	if len(split) < 2 {
		return nil
	}
	owner, repo := split[0], split[1]

	sender := c.Sender()
	if sender == nil {
		return nil
	}
	log.Printf("[Mode] sender ID=%d", sender.ID)

	dbUser, err := db.FindUserByTelegramID(sender.ID)
	if err != nil {
		log.Printf("[Mode] DB error finding user %d: %v", sender.ID, err)
	}
	if dbUser == nil {
		log.Printf("[Mode] User not found for TelegramID=%d", sender.ID)
		return nil
	}

	repoData, err := db.FindRepoByUserAndFullName(dbUser.ID, owner, repo)
	if err != nil {
		log.Printf("[Mode] DB error finding repo %s: %v", repoFullName, err)
	}
	if repoData == nil {
		log.Printf("[Mode] Repo not found: %s for userID=%d", repoFullName, dbUser.ID)
		return nil
	}
	log.Printf("[Mode] Found repo ID=%d", repoData.ID)

	// Resolve short SHA → full 40-char SHA
	fullSHA, err := services.ResolveCommitSHA(owner, repo, sha12)
	if err != nil {
		log.Printf("[Mode] Could not resolve SHA %s: %v — using partial", sha12, err)
		fullSHA = sha12
	}

	// Store pending state and show platform selection menu
	pb := &PendingBuild{
		RepoID:       repoData.ID,
		RepoFullName: repoFullName,
		FullSHA:      fullSHA,
		SHA12:        sha12,
		ImageName:    repoData.ImageName,
		BuildMode:    buildMode,
		Platforms:    "amd64", // sensible default
		Features:     map[string]bool{},
	}
	StorePending(pb)

	modeEmoji, modeLabel := modeInfo(buildMode)
	editText := fmt.Sprintf(
		"%s *%s:* `%s`\n🔗 Commit: `%s`\n\n🖥️ *Chọn platform build:*",
		modeEmoji, modeLabel, repoFullName, sha12[:min(7, len(sha12))],
	)
	kb := BuildPlatformKeyboard(pb)
	if editErr := c.Edit(editText, tele.ModeMarkdown, kb); editErr != nil {
		log.Printf("[Mode] Edit failed: %v — sending new message", editErr)
		if _, sendErr := c.Bot().Send(c.Chat(), editText, tele.ModeMarkdown, kb); sendErr != nil {
			log.Printf("[Mode] Send also failed: %v", sendErr)
		}
	}
	log.Printf("[Mode] Platform menu shown for %s @ %s mode=%s", repoFullName, sha12, buildMode)
	return nil
}

// ── plat: ─────────────────────────────────────────────────────────────────────

// handlePlatCallback — format: plat:{arch|next}:{owner/repo}:{sha12}
func handlePlatCallback(c tele.Context, data string) error {
	// Respond immediately for fast UX
	_ = c.Respond(&tele.CallbackResponse{})

	// data = "plat:amd64:owner/repo:sha12" or "plat:next:owner/repo:sha12"
	// Split into at most 3 parts after "plat:"
	rest := strings.TrimPrefix(data, "plat:")
	colonIdx := strings.Index(rest, ":")
	if colonIdx < 0 {
		return nil
	}
	action := rest[:colonIdx]       // "amd64", "arm64", "both", or "next"
	repoAndSha := rest[colonIdx+1:] // "owner/repo:sha12"

	// Extract sha12 as last colon-separated token
	lastColon := strings.LastIndex(repoAndSha, ":")
	if lastColon < 0 {
		return nil
	}
	repoFull := repoAndSha[:lastColon]
	sha12 := repoAndSha[lastColon+1:]

	pb := GetPending(repoFull, sha12)
	if pb == nil {
		_ = c.Edit("⚠️ Session expired. Please click Build again.", tele.ModeMarkdown)
		return nil
	}

	modeEmoji, modeLabel := modeInfo(pb.BuildMode)

	switch action {
	case "amd64", "arm64", "both":
		pb.Platforms = action
		editText := fmt.Sprintf(
			"%s *%s:* `%s`\n🔗 Commit: `%s`\n\n🖥️ *Chọn platform build:*",
			modeEmoji, modeLabel, repoFull, sha12[:min(7, len(sha12))],
		)
		kb := BuildPlatformKeyboard(pb)
		_ = c.Edit(editText, tele.ModeMarkdown, kb)

	case "next":
		// Move to feature selection
		editText := fmt.Sprintf(
			"%s *%s:* `%s`\n🔗 Commit: `%s`\n📦 Platform: `%s`\n\n🛠️ *Chọn optional features:*",
			modeEmoji, modeLabel, repoFull, sha12[:min(7, len(sha12))], platLabel(pb.Platforms),
		)
		kb := BuildFeatureKeyboard(pb)
		if editErr := c.Edit(editText, tele.ModeMarkdown, kb); editErr != nil {
			log.Printf("[Plat] Edit failed: %v", editErr)
		}
		log.Printf("[Plat] Feature menu shown for %s @ %s plat=%s", repoFull, sha12, pb.Platforms)
	}
	return nil
}

// platLabel converts arch key to a display string.
func platLabel(p string) string {
	switch p {
	case "arm64":
		return "linux/arm64"
	case "both":
		return "linux/amd64 + linux/arm64"
	default:
		return "linux/amd64"
	}
}

// ── feat: ─────────────────────────────────────────────────────────────────────

// handleFeatCallback — format: feat:toggle:owner/repo:sha12:featureKey or feat:build:owner/repo:sha12
func handleFeatCallback(c tele.Context, data string) error {
	// Respond immediately for fast UX
	_ = c.Respond(&tele.CallbackResponse{})

	parts := strings.SplitN(data, ":", 3)
	if len(parts) < 3 {
		return nil
	}
	action := parts[1] // "toggle" or "build"
	rest := parts[2]   // "owner/repo:sha12[:featureKey]"

	switch action {
	case "toggle":
		// rest = "owner/repo:sha12:featureKey"
		lastColon := strings.LastIndex(rest, ":")
		if lastColon < 0 {
			return nil
		}
		pbKey := rest[:lastColon] // "owner/repo:sha12"
		featureKey := rest[lastColon+1:]

		// pbKey format = "owner/repo:sha12"
		colonIdx := strings.LastIndex(pbKey, ":")
		repoFull := pbKey[:colonIdx]
		sha12 := pbKey[colonIdx+1:]

		pb := GetPending(repoFull, sha12)
		if pb == nil {
			_ = c.Edit("⚠️ Session expired. Please click Build again.", tele.ModeMarkdown)
			return nil
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
		modeEmoji, modeLabel := modeInfo(pb.BuildMode)
		editText := fmt.Sprintf(
			"%s *%s:* `%s`\n🔗 Commit: `%s`\n📦 Platform: `%s`\n\n🛠️ *Chọn optional features:*",
			modeEmoji, modeLabel, repoFull, sha12[:min(7, len(sha12))], platLabel(pb.Platforms),
		)
		_ = c.Edit(editText, tele.ModeMarkdown, kb)
		log.Printf("[Feat] toggle %s → %s", label, status)

	case "build":
		// New format: rest = "mode:owner/repo:sha12"
		buildModeAndRest := strings.SplitN(rest, ":", 2)
		if len(buildModeAndRest) < 2 {
			return nil
		}
		buildMode := buildModeAndRest[0]  // "local" or "actions"
		repoAndSha := buildModeAndRest[1] // "owner/repo:sha12"

		colonIdx := strings.LastIndex(repoAndSha, ":")
		if colonIdx < 0 {
			return nil
		}
		repoFull := repoAndSha[:colonIdx]
		sha12 := repoAndSha[colonIdx+1:]

		// Try to get pending state (features + platform selected by user)
		pb := GetPending(repoFull, sha12)

		var selectedFeatures []string
		var repoID int64
		var fullSHA, imageName, platforms string

		if pb != nil {
			for _, f := range services.AvailableFeatures {
				if pb.Features[f.Key] {
					selectedFeatures = append(selectedFeatures, f.Key)
				}
			}
			repoID = pb.RepoID
			fullSHA = pb.FullSHA
			imageName = pb.ImageName
			platforms = pb.Platforms
			DeletePending(repoFull, sha12)
		} else {
			// Fallback: server restarted, look up from DB
			log.Printf("[Feat] Pending state lost for %s @ %s — doing DB lookup", repoFull, sha12)
			sender := c.Sender()
			if sender == nil {
				return nil
			}
			dbUser, _ := db.FindUserByTelegramID(sender.ID)
			if dbUser == nil {
				return nil
			}
			repoParts := strings.SplitN(repoFull, "/", 2)
			if len(repoParts) < 2 {
				return nil
			}
			repoData, _ := db.FindRepoByUserAndFullName(dbUser.ID, repoParts[0], repoParts[1])
			if repoData == nil {
				return nil
			}
			repoID = repoData.ID
			imageName = repoData.ImageName
			fullSHA = sha12 // best we can do without pending state
			platforms = "amd64"
		}

		services.AddToQueueWithFeatures(repoID, repoFull, fullSHA, imageName, buildMode, platforms, selectedFeatures)

		featDesc := "none"
		if len(selectedFeatures) > 0 {
			featDesc = strings.Join(selectedFeatures, ", ")
		}
		buildEmoji, buildLabel := modeInfo(buildMode)
		editText := fmt.Sprintf(
			"%s *%s Queued!*\n\n📦 Repository: `%s`\n🔗 Commit: `%s`\n🖥️ Platform: `%s`\n🛠️ Features: `%s`",
			buildEmoji, buildLabel+" Build", repoFull, sha12[:min(7, len(sha12))], platLabel(platforms), featDesc,
		)
		if editErr := c.Edit(editText, tele.ModeMarkdown); editErr != nil {
			log.Printf("[Feat] Edit failed on build confirm: %v", editErr)
			_ = c.Send(editText, tele.ModeMarkdown)
		}
		log.Printf("[Feat] %s build queued for %s @ %s plat=%s features=%v", buildMode, repoFull, sha12, platforms, selectedFeatures)
	}

	return nil
}

// ── skip: ─────────────────────────────────────────────────────────────────────

// handleSkipCallback — format: skip:owner/repo
func handleSkipCallback(c tele.Context, data string) error {
	_ = c.Respond(&tele.CallbackResponse{})

	parts := strings.SplitN(data, ":", 2)
	if len(parts) < 2 {
		return nil
	}
	repoFullName := parts[1]
	split := strings.SplitN(repoFullName, "/", 2)
	if len(split) < 2 {
		return nil
	}
	owner, repo := split[0], split[1]

	sender := c.Sender()
	if sender == nil {
		return nil
	}

	dbUser, _ := db.FindUserByTelegramID(sender.ID)
	if dbUser == nil {
		return nil
	}

	repoData, _ := db.FindRepoByUserAndFullName(dbUser.ID, owner, repo)
	if repoData != nil {
		_ = db.DeleteConfirmationsByRepo(repoData.ID)
	}

	editText := fmt.Sprintf("⏭️ *Update Skipped*\n\n📦 Repository: `%s`", repoFullName)
	_ = c.Edit(editText, tele.ModeMarkdown)
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
