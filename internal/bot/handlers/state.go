package handlers

import (
	"fmt"
	"sync"

	"github.com/nhd/autobuildtodocker/internal/services"
	tele "gopkg.in/telebot.v3"
)

// PendingBuild holds state for a build awaiting platform + feature selection.
type PendingBuild struct {
	RepoID       int64
	RepoFullName string // "owner/repo"
	FullSHA      string // full 40-char SHA
	SHA12        string // 12-char truncated (used as map key suffix)
	ImageName    string
	BuildMode    string          // "local" or "actions"
	Platforms    string          // "amd64", "arm64", or "both" (empty = not chosen yet)
	Features     map[string]bool // feature key → selected
}

var (
	pendingMu     sync.Mutex
	pendingBuilds = map[string]*PendingBuild{} // key: "owner/repo:sha12"
)

func pendingKey(repoFull, sha12 string) string {
	return repoFull + ":" + sha12
}

// StorePending saves a pending build state.
func StorePending(pb *PendingBuild) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	pendingBuilds[pendingKey(pb.RepoFullName, pb.SHA12)] = pb
}

// GetPending retrieves a pending build state.
func GetPending(repoFull, sha12 string) *PendingBuild {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	return pendingBuilds[pendingKey(repoFull, sha12)]
}

// DeletePending removes a pending build state.
func DeletePending(repoFull, sha12 string) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	delete(pendingBuilds, pendingKey(repoFull, sha12))
}

// modeInfo returns (emoji, label) for a build mode string.
func modeInfo(mode string) (string, string) {
	if mode == "actions" {
		return "🚀", "GitHub Actions"
	}
	return "🖥️", "Local"
}

// BuildPlatformKeyboard shows platform selection: amd64 / arm64 / both.
// callback format: plat:{arch}:{owner/repo}:{sha12}
// "Next" button format: plat:next:{owner/repo}:{sha12}
func BuildPlatformKeyboard(pb *PendingBuild) *tele.ReplyMarkup {
	kb := &tele.ReplyMarkup{}
	key := pendingKey(pb.RepoFullName, pb.SHA12)

	check := func(arch string) string {
		if pb.Platforms == arch {
			return "✅ "
		}
		return ""
	}

	kb.InlineKeyboard = [][]tele.InlineButton{
		{
			{Text: check("amd64") + "linux/amd64", Data: fmt.Sprintf("plat:amd64:%s", key)},
			{Text: check("arm64") + "linux/arm64", Data: fmt.Sprintf("plat:arm64:%s", key)},
		},
		{
			{Text: check("both") + "amd64 + arm64", Data: fmt.Sprintf("plat:both:%s", key)},
		},
		{
			{Text: "Next: Features →", Data: fmt.Sprintf("plat:next:%s", key)},
		},
	}
	return kb
}

// BuildFeatureKeyboard generates the feature selection inline keyboard for a pending build.
func BuildFeatureKeyboard(pb *PendingBuild) *tele.ReplyMarkup {
	kb := &tele.ReplyMarkup{}
	key := pendingKey(pb.RepoFullName, pb.SHA12) // "owner/repo:sha12"

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
			// NOTE: No Unique field — telebot would prepend \f{Unique}| to Data,
			// which can push past Telegram's 64-byte callback_data limit.
			// Our generic OnCallback handler routes by Data prefix instead.
			btn := tele.InlineButton{
				Text: label,
				Data: fmt.Sprintf("feat:toggle:%s:%s", key, f.Key),
			}
			row = append(row, btn)
		}
		rows = append(rows, row)
	}

	// Build Now — encode buildMode in data to survive server restarts
	// format: feat:build:{mode}:{owner/repo}:{sha12}
	buildBtn := tele.InlineButton{
		Text: "▶️ Build Now",
		Data: fmt.Sprintf("feat:build:%s:%s", pb.BuildMode, key),
	}
	rows = append(rows, []tele.InlineButton{buildBtn})
	kb.InlineKeyboard = rows
	return kb
}
