package services

import (
	"fmt"
	"log"

	"github.com/nhd/autobuildtodocker/internal/db"
	"github.com/robfig/cron/v3"
)

var schedulerCron *cron.Cron

// StartScheduler starts a cron job that checks repos every intervalMinutes.
func StartScheduler(intervalMinutes int) {
	if schedulerCron != nil {
		log.Println("Scheduler already running")
		return
	}

	schedulerCron = cron.New()
	spec := fmt.Sprintf("*/%d * * * *", intervalMinutes)

	_, err := schedulerCron.AddFunc(spec, func() {
		log.Println("[Scheduler] Running periodic repository check...")
		if err := CheckAllRepositories(); err != nil {
			log.Printf("[Scheduler] Error during periodic check: %v", err)
		}
	})
	if err != nil {
		log.Printf("[Scheduler] Failed to add cron job: %v", err)
		return
	}

	schedulerCron.Start()
	log.Printf("Scheduler started — every %d minutes", intervalMinutes)
}

// StopScheduler stops the cron scheduler.
func StopScheduler() {
	if schedulerCron != nil {
		schedulerCron.Stop()
		schedulerCron = nil
		log.Println("Scheduler stopped")
	}
}

// CheckAllRepositories checks all active repos for new commits/releases.
func CheckAllRepositories() error {
	repos, err := db.FindAllActiveRepos()
	if err != nil {
		return fmt.Errorf("fetch repos: %w", err)
	}
	if len(repos) == 0 {
		log.Println("[Scheduler] No active repositories to check")
		return nil
	}

	log.Printf("[Scheduler] Checking %d repositories...", len(repos))
	for _, repo := range repos {
		if err := checkRepository(repo); err != nil {
			log.Printf("[Scheduler] Error checking %s/%s: %v", repo.Owner, repo.Repo, err)
		}
	}
	log.Println("[Scheduler] Completed repository checks")
	return nil
}

func checkRepository(repo db.Repository) error {
	fullName := fmt.Sprintf("%s/%s", repo.Owner, repo.Repo)
	log.Printf("[Scheduler] Checking %s (branch: %s)...", fullName, repo.Branch)

	// Get user
	user, err := db.GetUserByID(repo.UserID)
	if err != nil || user == nil {
		return fmt.Errorf("user not found for repo %s", fullName)
	}

	// Check new commit
	latestCommit, err := GetLatestCommit(repo.Owner, repo.Repo, repo.Branch)
	if err != nil {
		return fmt.Errorf("get latest commit: %w", err)
	}

	if repo.LastCommitSHA.Valid && latestCommit != repo.LastCommitSHA.String {
		log.Printf("[Scheduler] New commit detected for %s: %s", fullName, latestCommit)

		_ = db.CreateConfirmation(repo.ID, "commit", latestCommit, 0)
		_ = db.UpdateRepoLastCommit(repo.ID, latestCommit)

		_ = NotifyUser(user.TelegramID, UpdateNotification{
			Type:      "commit",
			Repo:      fullName,
			Branch:    repo.Branch,
			SHA:       latestCommit,
			ImageName: repo.ImageName,
		})
		log.Printf("[Scheduler] Notification sent for new commit on %s", fullName)
	}

	// Check new release
	release, err := GetLatestRelease(repo.Owner, repo.Repo)
	if err != nil {
		return fmt.Errorf("get latest release: %w", err)
	}
	if release != nil && (!repo.LastReleaseTag.Valid || release.Tag != repo.LastReleaseTag.String) {
		log.Printf("[Scheduler] New release detected for %s: %s", fullName, release.Tag)

		_ = db.CreateConfirmation(repo.ID, "release", release.SHA, 0)
		_ = db.UpdateRepoLastRelease(repo.ID, release.Tag)

		_ = NotifyUser(user.TelegramID, UpdateNotification{
			Type:      "release",
			Repo:      fullName,
			Tag:       release.Tag,
			SHA:       release.SHA,
			ImageName: repo.ImageName,
		})
		log.Printf("[Scheduler] Notification sent for new release on %s", fullName)
	}
	return nil
}

// CheckRepositoryByID manually triggers a check for a specific repository.
func CheckRepositoryByID(repoID int64) error {
	repo, err := db.FindRepoByID(repoID)
	if err != nil {
		return err
	}
	if repo == nil {
		return fmt.Errorf("repository not found: %d", repoID)
	}
	if !repo.IsActive {
		log.Printf("[Scheduler] Repository %d is not active, skipping", repoID)
		return nil
	}
	return checkRepository(*repo)
}
