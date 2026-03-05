package services

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/nhd/autobuildtodocker/internal/config"
	"github.com/nhd/autobuildtodocker/internal/db"
)

// ─── Build Job ───────────────────────────────────────────────────────────────

type BuildJob struct {
	RepoID       int64
	RepoName     string // "owner/repo"
	CommitSHA    string
	ImageName    string
	ChatID       int64 // Telegram user to notify
	BuildID      int64 // DB row ID
	DispatchedAt time.Time
	BuildMode    string   // "local" or "actions"
	Features     []string // optional addon features for local builds
}

// ─── Queue ───────────────────────────────────────────────────────────────────

var (
	jobQueue  = make(chan *BuildJob, 100)
	queueOnce sync.Once

	statsMu sync.RWMutex
	stats   = map[string]int{
		"queued":     0,
		"dispatched": 0,
		"completed":  0,
		"failed":     0,
	}
)

// StartQueue starts the background queue worker goroutine.
func StartQueue() {
	queueOnce.Do(func() {
		go queueWorker()
		log.Println("Build queue worker started (local + GitHub Actions modes)")
	})
}

// GetQueueStats returns a snapshot of queue statistics.
func GetQueueStats() map[string]int {
	statsMu.RLock()
	defer statsMu.RUnlock()
	snapshot := make(map[string]int, len(stats))
	for k, v := range stats {
		snapshot[k] = v
	}
	snapshot["total"] = snapshot["queued"] + snapshot["dispatched"] + snapshot["completed"] + snapshot["failed"]
	return snapshot
}

// AddToQueue enqueues a new build job.
// buildMode: "local" = build on this server, "actions" = dispatch to GitHub Actions.
func AddToQueue(repoID int64, repoName, commitSHA, imageName, buildMode string) {
	AddToQueueWithFeatures(repoID, repoName, commitSHA, imageName, buildMode, nil)
}

// AddToQueueWithFeatures enqueues a build job with optional addon features (local mode only).
func AddToQueueWithFeatures(repoID int64, repoName, commitSHA, imageName, buildMode string, features []string) {
	if buildMode == "" {
		buildMode = "actions"
	}
	repo, err := db.FindRepoByID(repoID)
	if err != nil || repo == nil {
		log.Printf("[Queue] Cannot find repo %d: %v", repoID, err)
		return
	}
	user, err := db.GetUserByID(repo.UserID)
	if err != nil || user == nil {
		log.Printf("[Queue] Cannot find user for repo %d", repoID)
		return
	}

	buildID, err := db.CreateBuild(repoID, commitSHA, "queued")
	if err != nil {
		log.Printf("[Queue] Failed to create build record: %v", err)
		return
	}

	job := &BuildJob{
		RepoID:    repoID,
		RepoName:  repoName,
		CommitSHA: commitSHA,
		ImageName: imageName,
		ChatID:    user.TelegramID,
		BuildID:   buildID,
		BuildMode: buildMode,
		Features:  features,
	}

	statsMu.Lock()
	stats["queued"]++
	statsMu.Unlock()

	modeLabel := "GitHub Actions"
	if buildMode == "local" {
		modeLabel = "Local Server"
	}
	_ = SendBuildStatus(user.TelegramID, BuildStatus{
		Repo:      repoName,
		Status:    "pending",
		ImageName: imageNameWithRegistry(imageName),
		Message:   fmt.Sprintf("Queued for %s build...", modeLabel),
	})

	jobQueue <- job
	log.Printf("[Queue] Job enqueued [%s]: %s @ %s", buildMode, repoName, commitSHA[:7])
}

// ─── Worker ──────────────────────────────────────────────────────────────────

func queueWorker() {
	for job := range jobQueue {
		processJob(job)
	}
}

func processJob(job *BuildJob) {
	if job.BuildMode == "local" {
		processLocalBuild(job)
	} else {
		processActionsBuild(job)
	}
}

// processLocalBuild — clone + docker build + push on this server.
func processLocalBuild(job *BuildJob) {
	log.Printf("[Queue] Local build for %s @ %s", job.RepoName, job.CommitSHA[:7])

	statsMu.Lock()
	stats["queued"]--
	stats["dispatched"]++
	statsMu.Unlock()

	_ = db.UpdateBuildStatus(job.BuildID, "building", "")
	_ = SendBuildStatus(job.ChatID, BuildStatus{
		Repo:    job.RepoName,
		Status:  "running",
		Message: "🖥️ Building locally on server...",
	})

	repo, _ := db.FindRepoByID(job.RepoID)
	dockerfilePath := "Dockerfile"
	if repo != nil && repo.DockerfilePath != "" {
		dockerfilePath = repo.DockerfilePath
	}

	result := BuildAndPush(BuildRequest{
		RepoID:         job.RepoID,
		RepoFullName:   job.RepoName,
		CommitSHA:      job.CommitSHA,
		ImageName:      job.ImageName,
		DockerfilePath: dockerfilePath,
	})

	statsMu.Lock()
	stats["dispatched"]--
	statsMu.Unlock()

	if result.Success {
		// Build addon layer if features were selected
		if len(job.Features) > 0 {
			_ = SendBuildStatus(job.ChatID, BuildStatus{
				Repo:    job.RepoName,
				Status:  "running",
				Message: fmt.Sprintf("🛠️ Installing features: %s...", strings.Join(job.Features, ", ")),
			})
			if err := BuildAddonLayer(job.ImageName, job.Features); err != nil {
				log.Printf("[Queue] Addon build failed for %s: %v", job.RepoName, err)
				_ = db.UpdateBuildStatus(job.BuildID, "failed", err.Error())
				statsMu.Lock()
				stats["dispatched"]--
				stats["failed"]++
				statsMu.Unlock()
				_ = SendBuildStatus(job.ChatID, BuildStatus{
					Repo:    job.RepoName,
					Status:  "failed",
					Message: "❌ Feature addon build failed: " + err.Error(),
				})
				return
			}
			// Re-push with addon layer
			if pushedImage, pushErr := pushImage(job.ImageName); pushErr != nil {
				log.Printf("[Queue] Addon push failed for %s: %v", job.RepoName, pushErr)
			} else {
				result.ImageName = pushedImage
			}
		}

		_ = db.UpdateBuildStatus(job.BuildID, "success", "")
		statsMu.Lock()
		stats["dispatched"]--
		stats["completed"]++
		statsMu.Unlock()
		featMsg := ""
		if len(job.Features) > 0 {
			featMsg = fmt.Sprintf(" (+%s)", strings.Join(job.Features, ", "))
		}
		_ = SendBuildStatus(job.ChatID, BuildStatus{
			Repo:      job.RepoName,
			Status:    "success",
			ImageName: result.ImageName,
			Message:   "✅ Local build & push completed!" + featMsg,
		})
	} else {
		_ = db.UpdateBuildStatus(job.BuildID, "failed", result.Error)
		statsMu.Lock()
		stats["dispatched"]--
		stats["failed"]++
		statsMu.Unlock()
		_ = SendBuildStatus(job.ChatID, BuildStatus{
			Repo:    job.RepoName,
			Status:  "failed",
			Message: "❌ Local build failed: " + result.Error,
		})
	}
}

// processActionsBuild — dispatch to GitHub Actions workflow.
func processActionsBuild(job *BuildJob) {
	log.Printf("[Queue] Dispatching GitHub Actions build for %s @ %s", job.RepoName, job.CommitSHA[:7])

	statsMu.Lock()
	stats["queued"]--
	stats["dispatched"]++
	statsMu.Unlock()

	_ = db.UpdateBuildStatus(job.BuildID, "dispatched", "")

	builderRepo := config.App.BuilderRepo
	if builderRepo == "" {
		errMsg := "BUILDER_REPO is not configured"
		log.Printf("[Queue] Error: %s", errMsg)
		_ = db.UpdateBuildStatus(job.BuildID, "failed", errMsg)
		_ = SendBuildStatus(job.ChatID, BuildStatus{
			Repo:    job.RepoName,
			Status:  "failed",
			Message: errMsg,
		})
		statsMu.Lock()
		stats["dispatched"]--
		stats["failed"]++
		statsMu.Unlock()
		return
	}

	imageName := slugifyImage(job.ImageName)

	dispatchedAt := time.Now().UTC()
	err := TriggerWorkflowDispatch(builderRepo, "docker-build.yml", "main", map[string]string{
		"repo":             job.RepoName,
		"commit_sha":       job.CommitSHA,
		"image_name":       imageName,
		"telegram_chat_id": fmt.Sprintf("%d", job.ChatID),
	})
	if err != nil {
		log.Printf("[Queue] Dispatch failed for %s: %v", job.RepoName, err)
		_ = db.UpdateBuildStatus(job.BuildID, "failed", "dispatch error: "+err.Error())
		_ = SendBuildStatus(job.ChatID, BuildStatus{
			Repo:    job.RepoName,
			Status:  "failed",
			Message: "GitHub Actions dispatch failed: " + err.Error(),
		})
		statsMu.Lock()
		stats["dispatched"]--
		stats["failed"]++
		statsMu.Unlock()
		return
	}

	log.Printf("[Queue] ✅ Dispatched workflow for %s", job.RepoName)
	_ = db.UpdateBuildStatus(job.BuildID, "dispatched", "")

	imageRef := fmt.Sprintf("ghcr.io/%s/%s:latest", strings.Split(builderRepo, "/")[0], imageName)
	_ = SendBuildStatus(job.ChatID, BuildStatus{
		Repo:      job.RepoName,
		Status:    "running",
		ImageName: imageRef,
		Message:   fmt.Sprintf("GitHub Actions started\\! [View run](https://github.com/%s/actions)", builderRepo),
	})

	go pollWorkflowResult(job, builderRepo, dispatchedAt)
}

// pollWorkflowResult polls GitHub API until the workflow run completes, then updates DB.
func pollWorkflowResult(job *BuildJob, builderRepo string, after time.Time) {
	// Give GitHub a moment to register the run
	time.Sleep(15 * time.Second)

	for attempt := 0; attempt < 40; attempt++ {
		run, err := GetLatestWorkflowRun(builderRepo, "docker-build.yml", after)
		if err != nil {
			log.Printf("[Queue] Poll error for %s: %v", job.RepoName, err)
		} else if run != nil && run.Status == "completed" {
			if run.Conclusion == "success" {
				_ = db.UpdateBuildStatus(job.BuildID, "success", "")
				statsMu.Lock()
				stats["dispatched"]--
				stats["completed"]++
				statsMu.Unlock()
			} else {
				_ = db.UpdateBuildStatus(job.BuildID, "failed", "")
				statsMu.Lock()
				stats["dispatched"]--
				stats["failed"]++
				statsMu.Unlock()
			}
			log.Printf("[Queue] Workflow run %d concluded: %s", run.ID, run.Conclusion)
			return
		}
		time.Sleep(30 * time.Second)
	}
	// Timeout — mark as unknown
	log.Printf("[Queue] Polling timeout for %s build %d", job.RepoName, job.BuildID)
	_ = db.UpdateBuildStatus(job.BuildID, "timeout", "")
	statsMu.Lock()
	stats["dispatched"]--
	stats["failed"]++
	statsMu.Unlock()
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func imageNameWithRegistry(imageName string) string {
	owner := config.App.Docker.Username
	if config.App.BuilderRepo != "" {
		owner = strings.Split(config.App.BuilderRepo, "/")[0]
	}
	return fmt.Sprintf("ghcr.io/%s/%s:latest", strings.ToLower(owner), slugifyImage(imageName))
}

func slugifyImage(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
