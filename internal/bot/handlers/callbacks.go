package handlers

import (
	"fmt"
	"log"
	"strconv"
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

// handleBuildCallback — format: build:{repoID}:{sha8} (from scheduler notification)
// Creates a session and shows a mode selection menu (Local / GitHub Actions).
func handleBuildCallback(c tele.Context, data string) error {
	// Respond immediately for snappy UX — Telegram shows loading until we do this.
	_ = c.Respond(&tele.CallbackResponse{})

	parts := strings.SplitN(data, ":", 3)
	if len(parts) < 3 {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid build request"})
	}
	repoIDStr := parts[1]
	commitSHA := parts[2]

	repoID, err := strconv.ParseInt(repoIDStr, 10, 64)
	if err != nil {
		log.Printf("[Build] Invalid repoID: %s", repoIDStr)
		return nil
	}

	repoData, _ := db.FindRepoByID(repoID)
	if repoData == nil {
		return nil
	}
	_ = db.DeleteConfirmationsByRepo(repoData.ID)

	repoFullName := fmt.Sprintf("%s/%s", repoData.Owner, repoData.Repo)

	// SHA for display (7 chars)
	sha7 := commitSHA
	if len(sha7) > 7 {
		sha7 = sha7[:7]
	}

	// Create a session early so mode buttons use short session IDs
	// (avoids 64-byte limit with long repo names)

	// Try to fetch the latest release tag for this repo
	versionTag := ""
	release, relErr := services.GetLatestRelease(repoData.Owner, repoData.Repo)
	if relErr == nil && release != nil {
		versionTag = release.Tag
		log.Printf("[Build] Found release tag for %s: %s", repoFullName, versionTag)
	}

	pb := &PendingBuild{
		RepoID:       repoData.ID,
		RepoFullName: repoFullName,
		FullSHA:      commitSHA,
		SHA7:         sha7,
		ImageName:    repoData.ImageName,
		Platforms:    "amd64", // sensible default
		Features:     map[string]bool{},
		VersionTag:   versionTag,
	}
	StorePending(pb)

	// Show mode selection: Local vs GitHub Actions
	// Uses session ID → max ~20 bytes callback data
	btnLocal := tele.InlineButton{
		Text: "🖥️ Local Server",
		Data: fmt.Sprintf("mode:local:%s", pb.SessionID),
	}
	btnActions := tele.InlineButton{
		Text: "🚀 GitHub Actions",
		Data: fmt.Sprintf("mode:actions:%s", pb.SessionID),
	}
	kb := &tele.ReplyMarkup{}
	kb.InlineKeyboard = [][]tele.InlineButton{{btnLocal, btnActions}}

	editText := fmt.Sprintf(
		"🐳 *Build:* `%s`\n🔗 Commit: `%s`\n\nChọn nơi build:",
		repoFullName, sha7,
	)
	if editErr := c.Edit(editText, tele.ModeMarkdown, kb); editErr != nil {
		log.Printf("[Build] Edit failed: %v — sending new message", editErr)
		_ = c.Send(editText, tele.ModeMarkdown, kb)
	}
	return nil
}

// ── mode: ─────────────────────────────────────────────────────────────────────

// handleModeCallback — format: mode:{local|actions}:{sessionID}
// Sets the build mode and shows platform selection.
func handleModeCallback(c tele.Context, data string) error {
	// Respond immediately — heavy work (DB + GitHub API) follows.
	_ = c.Respond(&tele.CallbackResponse{})

	log.Printf("[Mode] Parsing data: %q", data)
	parts := strings.SplitN(data, ":", 3)
	if len(parts) < 3 {
		log.Printf("[Mode] Invalid parts count: %d", len(parts))
		return nil
	}
	buildMode := parts[1] // "local" or "actions"
	sessionID := parts[2]
	log.Printf("[Mode] buildMode=%s session=%s", buildMode, sessionID)

	pb := GetPendingBySession(sessionID)
	if pb == nil {
		_ = c.Edit("⚠️ Session expired. Please click Build again.", tele.ModeMarkdown)
		return nil
	}

	// Set build mode
	pb.BuildMode = buildMode

	// Resolve short SHA → full 40-char SHA if we only have a partial
	repoFullName := pb.RepoFullName
	split := strings.SplitN(repoFullName, "/", 2)
	if len(split) >= 2 {
		fullSHA, err := services.ResolveCommitSHA(split[0], split[1], pb.FullSHA)
		if err != nil {
			log.Printf("[Mode] Could not resolve SHA %s: %v — using partial", pb.FullSHA, err)
		} else {
			pb.FullSHA = fullSHA
		}
	}

	modeEmoji, modeLabel := modeInfo(buildMode)
	editText := fmt.Sprintf(
		"%s *%s:* `%s`\n🔗 Commit: `%s`\n\n🖥️ *Chọn platform build:*",
		modeEmoji, modeLabel, repoFullName, pb.SHA7,
	)
	kb := BuildPlatformKeyboard(pb)
	if editErr := c.Edit(editText, tele.ModeMarkdown, kb); editErr != nil {
		log.Printf("[Mode] Edit failed: %v — sending new message", editErr)
		if _, sendErr := c.Bot().Send(c.Chat(), editText, tele.ModeMarkdown, kb); sendErr != nil {
			log.Printf("[Mode] Send also failed: %v", sendErr)
		}
	}
	log.Printf("[Mode] Platform menu shown for %s session=%s mode=%s", repoFullName, sessionID, buildMode)
	return nil
}

// ── plat: ─────────────────────────────────────────────────────────────────────

// handlePlatCallback — format: plat:{arch|next}:{sessionID}
func handlePlatCallback(c tele.Context, data string) error {
	// Respond immediately for fast UX
	_ = c.Respond(&tele.CallbackResponse{})

	parts := strings.SplitN(data, ":", 3)
	if len(parts) < 3 {
		return nil
	}
	action := parts[1] // "amd64", "arm64", "both", or "next"
	sessionID := parts[2]

	pb := GetPendingBySession(sessionID)
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
			modeEmoji, modeLabel, pb.RepoFullName, pb.SHA7,
		)
		kb := BuildPlatformKeyboard(pb)
		_ = c.Edit(editText, tele.ModeMarkdown, kb)

	case "next":
		// Move to feature selection
		editText := fmt.Sprintf(
			"%s *%s:* `%s`\n🔗 Commit: `%s`\n📦 Platform: `%s`\n\n🛠️ *Chọn optional features:*",
			modeEmoji, modeLabel, pb.RepoFullName, pb.SHA7, platLabel(pb.Platforms),
		)
		kb := BuildFeatureKeyboard(pb)
		if editErr := c.Edit(editText, tele.ModeMarkdown, kb); editErr != nil {
			log.Printf("[Plat] Edit failed: %v", editErr)
		}
		log.Printf("[Plat] Feature menu shown for %s session=%s plat=%s", pb.RepoFullName, sessionID, pb.Platforms)
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

// handleFeatCallback — format: feat:toggle:{sessionID}:{featureIndex} or feat:build:{sessionID}
func handleFeatCallback(c tele.Context, data string) error {
	// Respond immediately for fast UX
	_ = c.Respond(&tele.CallbackResponse{})

	parts := strings.SplitN(data, ":", 3)
	if len(parts) < 3 {
		return nil
	}
	action := parts[1] // "toggle" or "build"
	rest := parts[2]   // "{sessionID}:{featureIndex}" or "{sessionID}"

	switch action {
	case "toggle":
		// rest = "sessionID:featureIndex"
		colonIdx := strings.LastIndex(rest, ":")
		if colonIdx < 0 {
			return nil
		}
		sessionID := rest[:colonIdx]
		featureIdxStr := rest[colonIdx+1:]

		// Parse numeric feature index
		featureIdx, err := strconv.Atoi(featureIdxStr)
		if err != nil || featureIdx < 0 || featureIdx >= len(services.AvailableFeatures) {
			log.Printf("[Feat] Invalid feature index: %s", featureIdxStr)
			return nil
		}
		featureKey := services.AvailableFeatures[featureIdx].Key

		pb := GetPendingBySession(sessionID)
		if pb == nil {
			_ = c.Edit("⚠️ Session expired. Please click Build again.", tele.ModeMarkdown)
			return nil
		}

		// Toggle feature
		pb.Features[featureKey] = !pb.Features[featureKey]
		label := services.AvailableFeatures[featureIdx].Label
		status := "off"
		if pb.Features[featureKey] {
			status = "on"
		}

		kb := BuildFeatureKeyboard(pb)
		modeEmoji, modeLabel := modeInfo(pb.BuildMode)
		editText := fmt.Sprintf(
			"%s *%s:* `%s`\n🔗 Commit: `%s`\n📦 Platform: `%s`\n\n🛠️ *Chọn optional features:*",
			modeEmoji, modeLabel, pb.RepoFullName, pb.SHA7, platLabel(pb.Platforms),
		)
		_ = c.Edit(editText, tele.ModeMarkdown, kb)
		log.Printf("[Feat] toggle %s → %s", label, status)

	case "build":
		sessionID := rest

		// Try to get pending state (features + platform selected by user)
		pb := GetPendingBySession(sessionID)

		var selectedFeatures []string
		var repoID int64
		var fullSHA, imageName, platforms, buildMode, repoFull, sha7 string

		if pb != nil {
			for _, f := range services.AvailableFeatures {
				if pb.Features[f.Key] {
					selectedFeatures = append(selectedFeatures, f.Key)
				}
			}
			repoID = pb.RepoID
			repoFull = pb.RepoFullName
			fullSHA = pb.FullSHA
			imageName = pb.ImageName
			platforms = pb.Platforms
			buildMode = pb.BuildMode
			sha7 = pb.SHA7
			DeletePending(sessionID)
		} else {
			// Fallback: server restarted, look up from DB
			log.Printf("[Feat] Pending state lost for session %s — doing DB lookup", sessionID)
			_ = c.Edit("⚠️ Session expired. Please click Build again.", tele.ModeMarkdown)
			return nil
		}

		services.AddToQueueWithFeatures(repoID, repoFull, fullSHA, imageName, buildMode, platforms, selectedFeatures, pb.VersionTag)

		featDesc := "none"
		if len(selectedFeatures) > 0 {
			featDesc = strings.Join(selectedFeatures, ", ")
		}
		buildEmoji, buildLabel := modeInfo(buildMode)
		verLabel := ""
		if pb.VersionTag != "" {
			verLabel = fmt.Sprintf("\n🏷️ Version: `%s`", pb.VersionTag)
		}
		editText := fmt.Sprintf(
			"%s *%s Queued!*\n\n📦 Repository: `%s`\n🔗 Commit: `%s`\n🖥️ Platform: `%s`\n🛠️ Features: `%s`%s",
			buildEmoji, buildLabel+" Build", repoFull, sha7, platLabel(platforms), featDesc, verLabel,
		)
		if editErr := c.Edit(editText, tele.ModeMarkdown); editErr != nil {
			log.Printf("[Feat] Edit failed on build confirm: %v", editErr)
			_ = c.Send(editText, tele.ModeMarkdown)
		}
		log.Printf("[Feat] %s build queued for %s session=%s plat=%s features=%v", buildMode, repoFull, sessionID, platforms, selectedFeatures)
	}

	return nil
}

// ── skip: ─────────────────────────────────────────────────────────────────────

// handleSkipCallback — format: skip:{repoID}
func handleSkipCallback(c tele.Context, data string) error {
	_ = c.Respond(&tele.CallbackResponse{})

	parts := strings.SplitN(data, ":", 2)
	if len(parts) < 2 {
		return nil
	}

	repoID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		log.Printf("[Skip] Invalid repoID: %s", parts[1])
		return nil
	}

	repoData, _ := db.FindRepoByID(repoID)
	if repoData == nil {
		return nil
	}
	_ = db.DeleteConfirmationsByRepo(repoData.ID)

	repoFullName := fmt.Sprintf("%s/%s", repoData.Owner, repoData.Repo)
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
