package handlers

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/nhd/autobuildtodocker/internal/db"
	"github.com/nhd/autobuildtodocker/internal/services"
	tele "gopkg.in/telebot.v3"
)

// RegisterCommands registers all slash commands on the bot.
func RegisterCommands(bot *tele.Bot) {
	bot.Handle("/start", handleStart)
	bot.Handle("/add", handleAdd)
	bot.Handle("/list", handleList)
	bot.Handle("/remove", handleRemove)
	bot.Handle("/build", handleBuild)
	bot.Handle("/builds", handleBuilds)
	bot.Handle("/status", handleStatus)
	bot.Handle("/settings", handleSettings)
	bot.Handle("/help", handleStart) // alias
}

// RouteCommand dispatches a command string and args to the right handler.
// Used by the @mention text handler in groups.
func RouteCommand(cmd, args string, c tele.Context) error {
	// Inject args into Message.Payload so handlers pick it up naturally
	if msg := c.Message(); msg != nil {
		msg.Payload = args
	}
	switch strings.ToLower(cmd) {
	case "/start", "/help":
		return handleStart(c)
	case "/add":
		return handleAdd(c)
	case "/list":
		return handleList(c)
	case "/remove":
		return handleRemove(c)
	case "/build":
		return handleBuild(c)
	case "/builds":
		return handleBuilds(c)
	case "/status":
		return handleStatus(c)
	case "/settings":
		return handleSettings(c)
	default:
		return c.Send("❓ Unknown command: `"+cmd+"`. Type /help for available commands.", tele.ModeMarkdown)
	}
}

// ─── /start ──────────────────────────────────────────────────────────────────

func handleStart(c tele.Context) error {
	user := c.Sender()
	if user != nil {
		_ = db.UpsertUser(user.ID, user.Username, user.FirstName)
	}
	return c.Send(
		"👋 *Welcome to Docker Build Bot!*\n\n"+
			"I'll monitor your GitHub repositories and build Docker images automatically.\n\n"+
			"*Commands:*\n"+
			"`/add <owner/repo>` — Add a repository\n"+
			"`/list` — List your repositories\n"+
			"`/remove <owner/repo>` — Remove a repository\n"+
			"`/build <owner/repo>` — Trigger a manual build\n"+
			"`/builds [owner/repo]` — Show build history\n"+
			"`/status` — Show queue status\n"+
			"`/settings` — Bot settings\n"+
			"`/help` — Show this message",
		tele.ModeMarkdown,
	)
}

// ─── /add ────────────────────────────────────────────────────────────────────

var reGitHubURL = regexp.MustCompile(`(?:https?://github\.com/)?([a-zA-Z0-9_.-]+)/([a-zA-Z0-9_.-]+?)(?:\.git)?(?:@([a-zA-Z0-9_./%-]+))?$`)

func parseGitHubArg(arg string) (owner, repo, branch string, ok bool) {
	m := reGitHubURL.FindStringSubmatch(strings.TrimSpace(arg))
	if m == nil {
		return
	}
	return m[1], m[2], m[3], true
}

func slugify(s string) string {
	return regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(strings.ToLower(s), "-")
}

func handleAdd(c tele.Context) error {
	user := c.Sender()
	if user == nil {
		return c.Send("Unable to get user information.")
	}
	_ = db.UpsertUser(user.ID, user.Username, user.FirstName)

	args := strings.TrimSpace(c.Message().Payload)
	if args == "" {
		return c.Send("Usage: `/add <owner/repo> [image-name]`", tele.ModeMarkdown)
	}

	if !services.IsGitHubConfigured() {
		return c.Send("⚠️ GitHub API is not configured. Please set GITHUB_TOKEN.")
	}

	parts := strings.Fields(args)
	owner, repo, branch, ok := parseGitHubArg(parts[0])
	if !ok {
		return c.Send("❌ Invalid GitHub repository format. Use `owner/repo` or GitHub URL.", tele.ModeMarkdown)
	}

	_ = c.Send("🔍 Validating repository on GitHub...")

	ghRepo, err := services.ValidateRepo(owner, repo)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return c.Send(fmt.Sprintf("❌ Repository not found: `%s/%s`", owner, repo), tele.ModeMarkdown)
		}
		return c.Send("❌ Error: " + err.Error())
	}

	if branch == "" {
		branch = ghRepo.DefaultBranch
	}

	// Determine image name
	imageName := slugify(repo)
	if len(parts) >= 2 {
		imageName = slugify(parts[1])
	}

	// Get DB user
	dbUser, _ := db.FindUserByTelegramID(user.ID)
	if dbUser == nil {
		return c.Send("❌ User not found. Please /start first.")
	}

	// Check existing
	existing, _ := db.FindRepoByUserAndFullName(dbUser.ID, owner, repo)
	if existing != nil {
		return c.Send(fmt.Sprintf("ℹ️ Repository `%s/%s` is already tracked.\nBranch: `%s` | Image: `%s`",
			owner, repo, existing.Branch, existing.ImageName), tele.ModeMarkdown)
	}

	_, err = db.CreateRepo(dbUser.ID, owner, repo, branch, "Dockerfile", imageName, "docker.io", 60)
	if err != nil {
		return c.Send("❌ Failed to save repository: " + err.Error())
	}

	log.Printf("Repository added: %s/%s by @%s", owner, repo, user.Username)
	return c.Send(fmt.Sprintf(
		"✅ *Repository added!*\n\n"+
			"📦 `%s/%s`\n"+
			"🌿 Branch: `%s`\n"+
			"🐳 Image: `%s`",
		owner, repo, branch, imageName,
	), tele.ModeMarkdown)
}

// ─── /list ───────────────────────────────────────────────────────────────────

func handleList(c tele.Context) error {
	dbUser, _ := db.FindUserByTelegramID(c.Sender().ID)
	if dbUser == nil {
		return c.Send("Please run /start first.")
	}

	repos, err := db.FindReposByUser(dbUser.ID)
	if err != nil || len(repos) == 0 {
		return c.Send("📭 No repositories tracked yet. Use `/add <owner/repo>`.", tele.ModeMarkdown)
	}

	var sb strings.Builder
	sb.WriteString("📦 *Your Repositories:*\n\n")
	for i, r := range repos {
		status := "✅ Active"
		if !r.IsActive {
			status = "⏸️ Paused"
		}
		sb.WriteString(fmt.Sprintf("%d. `%s/%s`\n   Branch: `%s` | Image: `%s` | %s\n\n",
			i+1, r.Owner, r.Repo, r.Branch, r.ImageName, status))
	}
	return c.Send(sb.String(), tele.ModeMarkdown)
}

// ─── /remove ─────────────────────────────────────────────────────────────────

func handleRemove(c tele.Context) error {
	dbUser, _ := db.FindUserByTelegramID(c.Sender().ID)
	if dbUser == nil {
		return c.Send("Please run /start first.")
	}
	args := strings.TrimSpace(c.Message().Payload)
	if args == "" {
		return c.Send("Usage: `/remove <owner/repo>`", tele.ModeMarkdown)
	}
	owner, repo, _, ok := parseGitHubArg(args)
	if !ok {
		return c.Send("❌ Invalid format. Use `owner/repo`", tele.ModeMarkdown)
	}

	r, _ := db.FindRepoByUserAndFullName(dbUser.ID, owner, repo)
	if r == nil {
		return c.Send(fmt.Sprintf("❌ Repository `%s/%s` not found.", owner, repo), tele.ModeMarkdown)
	}

	if err := db.DeleteRepo(r.ID); err != nil {
		return c.Send("❌ Failed to remove: " + err.Error())
	}
	return c.Send(fmt.Sprintf("✅ Removed `%s/%s`", owner, repo), tele.ModeMarkdown)
}

// ─── /build ──────────────────────────────────────────────────────────────────

func handleBuild(c tele.Context) error {
	dbUser, _ := db.FindUserByTelegramID(c.Sender().ID)
	if dbUser == nil {
		return c.Send("Please run /start first.")
	}
	args := strings.TrimSpace(c.Message().Payload)
	if args == "" {
		return c.Send("Usage: `/build <owner/repo>`", tele.ModeMarkdown)
	}
	owner, repo, _, ok := parseGitHubArg(args)
	if !ok {
		return c.Send("❌ Invalid format.", tele.ModeMarkdown)
	}

	r, _ := db.FindRepoByUserAndFullName(dbUser.ID, owner, repo)
	if r == nil {
		return c.Send(fmt.Sprintf("❌ Repository `%s/%s` not tracked.", owner, repo), tele.ModeMarkdown)
	}

	commitSHA, err := services.GetLatestCommit(owner, repo, r.Branch)
	if err != nil {
		return c.Send("❌ Could not get latest commit: " + err.Error())
	}

	repoFull := fmt.Sprintf("%s/%s", owner, repo)

	// Inline keyboard: choose build mode
	btnLocal := tele.InlineButton{
		Text:   "🖥️ Local Server",
		Unique: "mode_local",
		Data:   fmt.Sprintf("mode:local:%s:%s", repoFull, commitSHA),
	}
	btnActions := tele.InlineButton{
		Text:   "🚀 GitHub Actions",
		Unique: "mode_actions",
		Data:   fmt.Sprintf("mode:actions:%s:%s", repoFull, commitSHA),
	}

	kb := &tele.ReplyMarkup{}
	kb.InlineKeyboard = [][]tele.InlineButton{{btnLocal, btnActions}}

	return c.Send(
		fmt.Sprintf("🐳 *Build:* `%s`\n🔗 Commit: `%s`\n\nChọn nơi build:", repoFull, commitSHA[:7]),
		tele.ModeMarkdown, kb,
	)
}

// ─── /builds ─────────────────────────────────────────────────────────────────

func handleBuilds(c tele.Context) error {
	dbUser, _ := db.FindUserByTelegramID(c.Sender().ID)
	if dbUser == nil {
		return c.Send("Please run /start first.")
	}

	args := strings.TrimSpace(c.Message().Payload)
	var builds []db.Build

	if args != "" {
		owner, repo, _, ok := parseGitHubArg(args)
		if !ok {
			return c.Send("❌ Invalid format.", tele.ModeMarkdown)
		}
		r, _ := db.FindRepoByUserAndFullName(dbUser.ID, owner, repo)
		if r == nil {
			return c.Send(fmt.Sprintf("❌ Repository `%s/%s` not tracked.", owner, repo), tele.ModeMarkdown)
		}
		builds, _ = db.FindBuildsByRepo(r.ID, 10)
	} else {
		builds, _ = db.FindAllBuildsRecent(10)
	}

	if len(builds) == 0 {
		return c.Send("📭 No builds yet.")
	}

	statusEmoji := map[string]string{
		"pending":    "⏳",
		"building":   "⚙️",
		"dispatched": "🚀",
		"success":    "✅",
		"failed":     "❌",
		"timeout":    "⏰",
	}

	var sb strings.Builder
	sb.WriteString("🏗️ *Recent Builds:*\n\n")
	for i, b := range builds {
		sha := "unknown"
		if b.CommitSHA.Valid {
			sha = b.CommitSHA.String[:7]
		}
		emoji := statusEmoji[b.BuildStatus]
		if emoji == "" {
			emoji = "❓"
		}
		sb.WriteString(fmt.Sprintf("%d. %s `%s` — `%s`\n   %s\n\n",
			i+1, emoji, sha, b.BuildStatus, b.StartedAt))
	}
	return c.Send(sb.String(), tele.ModeMarkdown)
}

// ─── /status ─────────────────────────────────────────────────────────────────

func handleStatus(c tele.Context) error {
	stats := services.GetQueueStats()
	return c.Send(fmt.Sprintf(
		"📊 *Queue Status*\n\n"+
			"Total: %d\n"+
			"⏳ Queued: %d\n"+
			"🚀 Dispatched: %d\n"+
			"✅ Completed: %d\n"+
			"❌ Failed: %d",
		stats["total"], stats["queued"], stats["dispatched"], stats["completed"], stats["failed"],
	), tele.ModeMarkdown)
}

// ─── /settings ───────────────────────────────────────────────────────────────

func handleSettings(c tele.Context) error {
	dbUser, _ := db.FindUserByTelegramID(c.Sender().ID)
	if dbUser == nil {
		return c.Send("Please run /start first.")
	}
	repos, _ := db.FindReposByUser(dbUser.ID)
	suffix := "ies"
	if len(repos) == 1 {
		suffix = "y"
	}
	return c.Send(fmt.Sprintf(
		"⚙️ *Settings*\n\nYou have %d repositor%s tracked.",
		len(repos), suffix,
	), tele.ModeMarkdown)
}
