package services

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nhd/autobuildtodocker/internal/db"
)

// ─── Build Job ────────────────────────────────────────────────────────────────

type JobStatus string

const (
	StatusQueued    JobStatus = "queued"
	StatusRunning   JobStatus = "running"
	StatusCompleted JobStatus = "completed"
	StatusFailed    JobStatus = "failed"
)

type BuildJob struct {
	ID           string
	RepoID       int64
	RepoFullName string
	CommitSHA    string
	ImageName    string
	UserID       int64
	TelegramID   int64
	Status       JobStatus
	CreatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
	Logs         string
	Error        string
	Retries      int
}

// ─── Queue ────────────────────────────────────────────────────────────────────

const maxRetries = 3

var retryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}

var (
	queueMu  sync.Mutex
	jobQueue []*BuildJob
	jobChan  = make(chan struct{}, 1) // signal to process
)

func init() {
	go queueWorker()
}

func generateJobID() string {
	return fmt.Sprintf("job_%d_%d", time.Now().UnixMilli(), time.Now().Nanosecond()%1_000_000)
}

// AddToQueue creates and enqueues a new build job.
func AddToQueue(repoID int64, repoFullName, commitSHA, imageName string) *BuildJob {
	job := &BuildJob{
		ID:           generateJobID(),
		RepoID:       repoID,
		RepoFullName: repoFullName,
		CommitSHA:    commitSHA,
		ImageName:    imageName,
		Status:       StatusQueued,
		CreatedAt:    time.Now(),
	}

	// Lookup user
	if repo, err := db.FindRepoByID(repoID); err == nil && repo != nil {
		job.UserID = repo.UserID
		if u, err := db.GetUserByID(repo.UserID); err == nil && u != nil {
			job.TelegramID = u.TelegramID
		}
	}

	// Create DB build record
	if _, err := db.CreateBuild(repoID, commitSHA, imageName); err != nil {
		log.Printf("[Queue] Failed to create build record: %v", err)
	}

	queueMu.Lock()
	jobQueue = append(jobQueue, job)
	queueMu.Unlock()

	log.Printf("[Queue] Added job %s for %s", job.ID, repoFullName)

	// Signal worker non-blocking
	select {
	case jobChan <- struct{}{}:
	default:
	}

	return job
}

// GetAllJobs returns a copy of all jobs.
func GetAllJobs() []*BuildJob {
	queueMu.Lock()
	defer queueMu.Unlock()
	result := make([]*BuildJob, len(jobQueue))
	copy(result, jobQueue)
	return result
}

// GetQueueStats returns queue statistics.
func GetQueueStats() map[string]int {
	queueMu.Lock()
	defer queueMu.Unlock()
	stats := map[string]int{"total": 0, "queued": 0, "running": 0, "completed": 0, "failed": 0}
	for _, j := range jobQueue {
		stats["total"]++
		stats[string(j.Status)]++
	}
	return stats
}

// ClearCompleted removes completed/failed jobs from in-memory queue.
func ClearCompleted() {
	queueMu.Lock()
	defer queueMu.Unlock()
	var remaining []*BuildJob
	for _, j := range jobQueue {
		if j.Status != StatusCompleted && j.Status != StatusFailed {
			remaining = append(remaining, j)
		}
	}
	cleared := len(jobQueue) - len(remaining)
	jobQueue = remaining
	log.Printf("[Queue] Cleared %d completed jobs", cleared)
}

// ─── Worker ───────────────────────────────────────────────────────────────────

func queueWorker() {
	for range jobChan {
		processQueue()
	}
}

func processQueue() {
	for {
		job := nextQueuedJob()
		if job == nil {
			return
		}
		setJobStatus(job, StatusRunning)
		log.Printf("[Queue] Processing job %s for %s", job.ID, job.RepoFullName)
		processJob(job)
	}
}

func nextQueuedJob() *BuildJob {
	queueMu.Lock()
	defer queueMu.Unlock()
	for _, j := range jobQueue {
		if j.Status == StatusQueued {
			return j
		}
	}
	return nil
}

func setJobStatus(job *BuildJob, status JobStatus) {
	queueMu.Lock()
	defer queueMu.Unlock()
	job.Status = status
	now := time.Now()
	switch status {
	case StatusRunning:
		job.StartedAt = &now
	case StatusCompleted, StatusFailed:
		job.CompletedAt = &now
	}
}

func processJob(job *BuildJob) {
	// Send start notification
	if job.TelegramID != 0 {
		_ = SendBuildStatus(job.TelegramID, BuildStatus{
			Repo:      job.RepoFullName,
			Status:    "running",
			ImageName: job.ImageName,
			Message:   fmt.Sprintf("Building commit %s...", job.CommitSHA[:7]),
		})
	}

	repo, _ := db.FindRepoByID(job.RepoID)
	dockerfilePath := "Dockerfile"
	if repo != nil && repo.DockerfilePath != "" {
		dockerfilePath = repo.DockerfilePath
	}

	req := BuildRequest{
		RepoID:         job.RepoID,
		RepoFullName:   job.RepoFullName,
		CommitSHA:      job.CommitSHA,
		ImageName:      job.ImageName,
		DockerfilePath: dockerfilePath,
	}

	result := BuildAndPush(req)

	if result.Success {
		setJobStatus(job, StatusCompleted)
		job.Logs = result.Logs

		// Update DB
		if b, err := db.FindLatestBuildByRepo(job.RepoID); err == nil && b != nil {
			_ = db.UpdateBuildStatus(b.ID, "success", result.Logs)
		}

		// Success notification
		if job.TelegramID != 0 {
			_ = SendBuildStatus(job.TelegramID, BuildStatus{
				Repo:      job.RepoFullName,
				Status:    "success",
				ImageName: result.ImageName,
				Message:   "Build completed successfully",
			})
		}
	} else {
		job.Retries++
		if job.Retries <= maxRetries {
			delay := retryDelays[job.Retries-1]
			log.Printf("[Queue] Build failed, retrying (%d/%d) in %s...", job.Retries, maxRetries, delay)
			time.AfterFunc(delay, func() {
				setJobStatus(job, StatusQueued)
				select {
				case jobChan <- struct{}{}:
				default:
				}
			})
			return
		}

		// Max retries exceeded
		log.Printf("[Queue] Job %s failed after %d retries", job.ID, maxRetries)
		setJobStatus(job, StatusFailed)
		job.Error = result.Error
		job.Logs = result.Logs

		if b, err := db.FindLatestBuildByRepo(job.RepoID); err == nil && b != nil {
			_ = db.UpdateBuildStatus(b.ID, "failed", result.Logs)
		}

		if job.TelegramID != 0 {
			_ = SendBuildStatus(job.TelegramID, BuildStatus{
				Repo:    job.RepoFullName,
				Status:  "failed",
				Message: fmt.Sprintf("Build failed after %d retries: %s", maxRetries, result.Error),
			})
		}
	}
}
