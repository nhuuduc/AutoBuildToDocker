package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/nhd/autobuildtodocker/internal/services"
	tele "gopkg.in/telebot.v3"
)

// PendingBuild holds state for a build awaiting platform + feature selection.
type PendingBuild struct {
	SessionID    string // short random ID used in callback data (8 hex chars)
	RepoID       int64
	RepoFullName string // "owner/repo"
	FullSHA      string // full 40-char SHA
	SHA7         string // 7-char truncated (for display only)
	ImageName    string
	BuildMode    string          // "local" or "actions"
	Platforms    string          // "amd64", "arm64", or "both" (empty = not chosen yet)
	Features     map[string]bool // feature key → selected
	VersionTag   string          // GitHub release tag (e.g. "v0.3.46"), empty if commit-only
}

var (
	pendingMu     sync.Mutex
	pendingBuilds = map[string]*PendingBuild{} // key: sessionID
)

// newSessionID generates a short random hex string (8 chars = 4 bytes).
func newSessionID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// StorePending saves a pending build state, generating a session ID if needed.
func StorePending(pb *PendingBuild) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if pb.SessionID == "" {
		pb.SessionID = newSessionID()
	}
	pendingBuilds[pb.SessionID] = pb
}

// GetPendingBySession retrieves a pending build state by session ID.
func GetPendingBySession(sessionID string) *PendingBuild {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	return pendingBuilds[sessionID]
}

// DeletePending removes a pending build state.
func DeletePending(sessionID string) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	delete(pendingBuilds, sessionID)
}

// modeInfo returns (emoji, label) for a build mode string.
func modeInfo(mode string) (string, string) {
	if mode == "actions" {
		return "🚀", "GitHub Actions"
	}
	return "🖥️", "Local"
}

// BuildPlatformKeyboard shows platform selection: amd64 / arm64 / both.
// All callback data uses short sessionID to stay well within 64-byte limit.
// callback format: plat:{arch}:{sessionID}
// "Next" button format: plat:next:{sessionID}
func BuildPlatformKeyboard(pb *PendingBuild) *tele.ReplyMarkup {
	kb := &tele.ReplyMarkup{}
	sid := pb.SessionID

	check := func(arch string) string {
		if pb.Platforms == arch {
			return "✅ "
		}
		return ""
	}

	kb.InlineKeyboard = [][]tele.InlineButton{
		{
			{Text: check("amd64") + "linux/amd64", Data: fmt.Sprintf("plat:amd64:%s", sid)},
			{Text: check("arm64") + "linux/arm64", Data: fmt.Sprintf("plat:arm64:%s", sid)},
		},
		{
			{Text: check("both") + "amd64 + arm64", Data: fmt.Sprintf("plat:both:%s", sid)},
		},
		{
			{Text: "Next: Features →", Data: fmt.Sprintf("plat:next:%s", sid)},
		},
	}
	return kb
}

// BuildFeatureKeyboard generates the feature selection inline keyboard for a pending build.
// Uses sessionID + numeric feature index to keep callback data very short.
// toggle format: feat:toggle:{sessionID}:{featureIndex}  (max ~25 bytes)
// build format:  feat:build:{sessionID}                   (max ~20 bytes)
func BuildFeatureKeyboard(pb *PendingBuild) *tele.ReplyMarkup {
	kb := &tele.ReplyMarkup{}
	sid := pb.SessionID

	var rows [][]tele.InlineButton
	feats := services.AvailableFeatures
	for i := 0; i < len(feats); i += 2 {
		var row []tele.InlineButton
		for j := i; j < i+2 && j < len(feats); j++ {
			f := feats[j]
			label := f.Emoji + " " + f.Label
			if pb.Features[f.Key] {
				label = "✅ " + f.Label
			}
			btn := tele.InlineButton{
				Text: label,
				Data: fmt.Sprintf("feat:toggle:%s:%d", sid, j),
			}
			row = append(row, btn)
		}
		rows = append(rows, row)
	}

	// Build Now
	buildBtn := tele.InlineButton{
		Text: "▶️ Build Now",
		Data: fmt.Sprintf("feat:build:%s", sid),
	}
	rows = append(rows, []tele.InlineButton{buildBtn})
	kb.InlineKeyboard = rows
	return kb
}
