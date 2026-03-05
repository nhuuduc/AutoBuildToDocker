package services

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nhd/autobuildtodocker/internal/config"
)

const githubAPIBase = "https://api.github.com"

// GitHubRepo holds basic repository info.
type GitHubRepo struct {
	Owner         string
	Repo          string
	FullName      string
	Description   string
	Private       bool
	DefaultBranch string
	URL           string
}

// GitHubRelease holds release info.
type GitHubRelease struct {
	Tag         string
	SHA         string
	Name        string
	PublishedAt string
}

// IsGitHubConfigured returns true if a GitHub token is set.
func IsGitHubConfigured() bool {
	return config.App.GitHub.Token != ""
}

func githubHeaders() map[string]string {
	h := map[string]string{
		"Accept":     "application/vnd.github.v3+json",
		"User-Agent": "DockerBuildBot",
	}
	if config.App.GitHub.Token != "" {
		h["Authorization"] = "token " + config.App.GitHub.Token
	}
	return h
}

func githubGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range githubHeaders() {
		req.Header.Set(k, v)
	}
	return http.DefaultClient.Do(req)
}

// ValidateRepo validates that owner/repo exists on GitHub.
func ValidateRepo(owner, repo string) (*GitHubRepo, error) {
	url := fmt.Sprintf("%s/repos/%s/%s", githubAPIBase, owner, repo)
	resp, err := githubGet(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 404:
		return nil, fmt.Errorf("repository not found: %s/%s", owner, repo)
	case 403:
		return nil, fmt.Errorf("GitHub API rate limit exceeded. Please try again later")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API error: %d %s", resp.StatusCode, resp.Status)
	}

	var data struct {
		Owner         struct{ Login string } `json:"owner"`
		Name          string                 `json:"name"`
		FullName      string                 `json:"full_name"`
		Description   string                 `json:"description"`
		Private       bool                   `json:"private"`
		DefaultBranch string                 `json:"default_branch"`
		HTMLURL       string                 `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &GitHubRepo{
		Owner:         data.Owner.Login,
		Repo:          data.Name,
		FullName:      data.FullName,
		Description:   data.Description,
		Private:       data.Private,
		DefaultBranch: data.DefaultBranch,
		URL:           data.HTMLURL,
	}, nil
}

// GetLatestCommit returns the latest commit SHA for owner/repo/branch.
func GetLatestCommit(owner, repo, branch string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/branches/%s", githubAPIBase, owner, repo, branch)
	resp, err := githubGet(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return "", fmt.Errorf("branch not found: %s", branch)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API error: %d %s", resp.StatusCode, resp.Status)
	}

	var data struct {
		Commit struct{ SHA string } `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	return data.Commit.SHA, nil
}

// GetLatestRelease returns the latest release for owner/repo, or nil if none.
func GetLatestRelease(owner, repo string) (*GitHubRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", githubAPIBase, owner, repo)
	resp, err := githubGet(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, nil // No releases – that's fine
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API error: %d %s", resp.StatusCode, resp.Status)
	}

	var data struct {
		TagName         string `json:"tag_name"`
		TargetCommitish string `json:"target_commitish"`
		Name            string `json:"name"`
		PublishedAt     string `json:"published_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	name := data.Name
	if name == "" {
		name = data.TagName
	}
	return &GitHubRelease{
		Tag:         data.TagName,
		SHA:         data.TargetCommitish,
		Name:        name,
		PublishedAt: data.PublishedAt,
	}, nil
}

// GetFileContent returns the decoded content of a file in the repo, or "" if not found.
func GetFileContent(owner, repo, path, branch string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", githubAPIBase, owner, repo, path, branch)
	resp, err := githubGet(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", nil
	}

	var data struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}
	if data.Encoding == "base64" && data.Content != "" {
		// GitHub adds newlines in base64 output
		cleaned := ""
		for _, c := range data.Content {
			if c != '\n' {
				cleaned += string(c)
			}
		}
		decoded, err := base64.StdEncoding.DecodeString(cleaned)
		if err != nil {
			return "", err
		}
		return string(decoded), nil
	}
	return "", nil
}

// ─── GitHub Actions ──────────────────────────────────────────────────────────

// WorkflowRun holds the status of a GitHub Actions workflow run.
type WorkflowRun struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`     // queued, in_progress, completed
	Conclusion string `json:"conclusion"` // success, failure, cancelled, ...
	HTMLURL    string `json:"html_url"`
	CreatedAt  string `json:"created_at"`
}

// TriggerWorkflowDispatch triggers a workflow_dispatch event on builderRepo.
// builderRepo format: "owner/repo"
// workflowFile: e.g. "docker-build.yml"
// ref: branch to use, e.g. "main"
// inputs: key/value inputs for the workflow
func TriggerWorkflowDispatch(builderRepo, workflowFile, ref string, inputs map[string]string) error {
	if config.App.GitHub.Token == "" {
		return fmt.Errorf("GITHUB_TOKEN is required to trigger GitHub Actions")
	}
	if builderRepo == "" {
		return fmt.Errorf("BUILDER_REPO is not configured")
	}

	parts := strings.SplitN(builderRepo, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid BUILDER_REPO format, expected owner/repo: %s", builderRepo)
	}
	owner, repo := parts[0], parts[1]

	payload := map[string]any{
		"ref":    ref,
		"inputs": inputs,
	}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/repos/%s/%s/actions/workflows/%s/dispatches",
		githubAPIBase, owner, repo, workflowFile)

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range githubHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 204 No Content = success
	if resp.StatusCode == 204 {
		return nil
	}
	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("workflow dispatch failed (%d): %s", resp.StatusCode, respBody)
}

// GetLatestWorkflowRun returns the most recent run of a workflow in builderRepo.
// It retries briefly to find a run created after the given time (to avoid stale runs).
func GetLatestWorkflowRun(builderRepo, workflowFile string, after time.Time) (*WorkflowRun, error) {
	parts := strings.SplitN(builderRepo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid BUILDER_REPO: %s", builderRepo)
	}
	owner, repo := parts[0], parts[1]

	url := fmt.Sprintf("%s/repos/%s/%s/actions/workflows/%s/runs?per_page=5",
		githubAPIBase, owner, repo, workflowFile)

	resp, err := githubGet(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API error: %d", resp.StatusCode)
	}

	var data struct {
		WorkflowRuns []WorkflowRun `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	for i := range data.WorkflowRuns {
		run := &data.WorkflowRuns[i]
		t, err := time.Parse(time.RFC3339, run.CreatedAt)
		if err != nil || t.Before(after) {
			continue
		}
		return run, nil
	}
	return nil, nil
}
