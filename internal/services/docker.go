package services

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nhd/autobuildtodocker/internal/config"
)

// isGHCR returns true when registry is ghcr.io.
func isGHCR() bool {
	return strings.HasPrefix(config.App.Docker.Registry, "ghcr.io")
}

// BuildRequest holds all info needed to build and push a Docker image.
type BuildRequest struct {
	RepoID         int64
	RepoFullName   string
	CommitSHA      string
	ImageName      string
	DockerfilePath string
	Branch         string
	VersionTag     string // GitHub release tag (e.g. "v0.3.46"), empty for commit-only builds
}

// BuildResult is the outcome of a build.
type BuildResult struct {
	Success   bool
	ImageName string
	Logs      string
	Error     string
}

var tempDir = filepath.Join(".", "temp", "builds")

func init() {
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		log.Printf("[Docker] Warning: could not create tempDir %s: %v", tempDir, err)
	}
}

// getImageWithTag returns the full image reference.
// - docker.io  : imageName:tag  (e.g. myuser/myimage:latest)
// - ghcr.io    : ghcr.io/owner/imageName:tag
// - other      : registry/imageName:tag
func getImageWithTag(imageName, tag string) string {
	registry := config.App.Docker.Registry
	switch {
	case registry == "docker.io" || registry == "":
		return imageName + ":" + tag
	case strings.HasPrefix(registry, "ghcr.io"):
		// owner comes from DOCKER_USERNAME (= GitHub username)
		owner := config.App.Docker.Username
		if owner == "" {
			return "ghcr.io/" + imageName + ":" + tag
		}
		return fmt.Sprintf("ghcr.io/%s/%s:%s", strings.ToLower(owner), imageName, tag)
	default:
		return registry + "/" + imageName + ":" + tag
	}
}

// runLogged runs a command and streams its output line-by-line to the Go log.
// prefix is shown before each output line, e.g. "[Docker/git]".
func runLogged(prefix string, args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s start failed: %w", args[0], err)
	}

	// Stream lines to log
	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			log.Printf("%s %s", prefix, scanner.Text())
		}
		pr.Close() // Close the read end of the pipe when scanning is done
	}()

	err := cmd.Wait()
	pw.Close() // Close the write end of the pipe after the command finishes
	if err != nil {
		return fmt.Errorf("%v failed: %w", args, err)
	}
	return nil
}

// cloneRepo clones the repository at a specific commit SHA.
// Uses git init + fetch FETCH_HEAD to reliably fetch any specific commit by full SHA.
func cloneRepo(repoFullName, commitSHA string) (string, error) {
	cloneDir := filepath.Join(tempDir, strings.ReplaceAll(repoFullName, "/", "_"))
	gitURL := "https://github.com/" + repoFullName + ".git"

	log.Printf("[Docker] Cloning %s at %s", repoFullName, commitSHA[:7])

	// Clean up existing directory
	_ = os.RemoveAll(cloneDir)
	if err := os.MkdirAll(cloneDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir failed: %w", err)
	}

	prefix := fmt.Sprintf("[Docker/git %s]", repoFullName)
	cmds := [][]string{
		{"git", "init", cloneDir},
		{"git", "-C", cloneDir, "remote", "add", "origin", gitURL},
		{"git", "-C", cloneDir, "fetch", "--depth", "1", "origin", commitSHA},
		{"git", "-C", cloneDir, "checkout", "FETCH_HEAD"},
	}
	for _, args := range cmds {
		if err := runLogged(prefix, args...); err != nil {
			return "", fmt.Errorf("git command failed: %w", err)
		}
	}
	log.Printf("[Docker] ✅ Cloned %s", repoFullName)
	return cloneDir, nil
}

// buildImage builds a Docker image and streams output to log.
// If versionTag is non-empty, the image is also tagged with that version.
func buildImage(contextDir, imageName, dockerfilePath, versionTag string) error {
	fullImage := getImageWithTag(imageName, "latest")
	dockerfile := filepath.Join(contextDir, dockerfilePath)

	if _, err := os.Stat(dockerfile); os.IsNotExist(err) {
		return fmt.Errorf("Dockerfile not found at %s", dockerfile)
	}

	log.Printf("[Docker] Building image: %s", fullImage)
	prefix := fmt.Sprintf("[Docker/build %s]", imageName)
	args := []string{"docker", "build", "--progress=plain", "-t", fullImage}

	// Add version tag if provided
	if versionTag != "" {
		versionImage := getImageWithTag(imageName, versionTag)
		args = append(args, "-t", versionImage)
		log.Printf("[Docker] Also tagging as: %s", versionImage)
	}

	args = append(args, "-f", dockerfile, contextDir)
	if err := runLogged(prefix, args...); err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}
	log.Printf("[Docker] ✅ Built: %s", fullImage)
	return nil
}

// pushImage logs into registry then pushes the image.
// For ghcr.io: uses GITHUB_TOKEN as password (DOCKER_PASSWORD can override).
// If versionTag is non-empty, the version-tagged image is also pushed.
func pushImage(imageName, versionTag string) (string, error) {
	fullImage := getImageWithTag(imageName, "latest")
	log.Printf("[Docker] Pushing image: %s", fullImage)

	cfg := config.App.Docker

	// Determine registry endpoint, username and password for login.
	var loginRegistry, loginUser, loginPass string

	if isGHCR() {
		loginRegistry = "ghcr.io"
		loginUser = cfg.Username // GitHub username
		loginPass = cfg.Password // DOCKER_PASSWORD overrides; fall back to GITHUB_TOKEN
		if loginPass == "" {
			loginPass = config.App.GitHub.Token
		}
	} else {
		loginRegistry = cfg.Registry
		if loginRegistry == "docker.io" || loginRegistry == "" {
			loginRegistry = "https://index.docker.io/v1/"
		}
		loginUser = cfg.Username
		loginPass = cfg.Password
	}

	if loginUser != "" && loginPass != "" {
		loginCmd := exec.Command("docker", "login", loginRegistry, "-u", loginUser, "--password-stdin")
		loginCmd.Stdin = strings.NewReader(loginPass)
		if out, err := loginCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("docker login failed: %w\n%s", err, out)
		}
	} else {
		log.Println("[Docker] No credentials provided, attempting anonymous push")
	}

	pushCmd := exec.Command("docker", "push", fullImage)
	out, err := pushCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker push failed: %w\n%s", err, out)
	}
	log.Printf("[Docker] Successfully pushed: %s", fullImage)

	// Also push version-tagged image if provided
	if versionTag != "" {
		versionImage := getImageWithTag(imageName, versionTag)
		log.Printf("[Docker] Also pushing version tag: %s", versionImage)
		vPushCmd := exec.Command("docker", "push", versionImage)
		if vOut, vErr := vPushCmd.CombinedOutput(); vErr != nil {
			log.Printf("[Docker] Warning: version tag push failed: %v\n%s", vErr, vOut)
			// Non-fatal — :latest was already pushed successfully
		} else {
			log.Printf("[Docker] Successfully pushed version tag: %s", versionImage)
		}
	}

	return fullImage, nil
}

// cleanup removes the temporary clone directory.
func cleanup(repoFullName string) {
	var cloneDir string
	if repoFullName == "local/ubuntu-os" {
		cloneDir = filepath.Join(tempDir, "local_ubuntu-os")
	} else {
		cloneDir = filepath.Join(tempDir, strings.ReplaceAll(repoFullName, "/", "_"))
	}

	if err := os.RemoveAll(cloneDir); err != nil {
		log.Printf("[Docker] Cleanup failed for %s: %v", cloneDir, err)
	} else {
		log.Printf("[Docker] Cleaned up: %s", cloneDir)
	}
}

// BuildAndPush executes the full clone → build → push workflow.
func BuildAndPush(req BuildRequest) BuildResult {
	var logs []string
	start := time.Now()
	ts := func() string { return time.Now().Format(time.RFC3339) }

	appendLog := func(msg string) {
		log.Println(msg)
		logs = append(logs, fmt.Sprintf("[%s] %s", ts(), msg))
	}

	appendLog(fmt.Sprintf("Starting build for %s", req.RepoFullName))
	appendLog(fmt.Sprintf("Commit: %s", req.CommitSHA[:7]))
	appendLog(fmt.Sprintf("Image: %s", req.ImageName))
	if req.VersionTag != "" {
		appendLog(fmt.Sprintf("Version: %s", req.VersionTag))
	}

	dockerfilePath := req.DockerfilePath
	if dockerfilePath == "" {
		dockerfilePath = "Dockerfile"
	}

	// Step 1: Clone or Generate Strategy
	var cloneDir string
	var err error

	if req.RepoFullName == "local/ubuntu-os" {
		appendLog("OS Build detected. Generating base Ubuntu Dockerfile...")
		cloneDir = filepath.Join(tempDir, "local_ubuntu-os")
		_ = os.RemoveAll(cloneDir)
		if err = os.MkdirAll(cloneDir, 0o755); err != nil {
			err = fmt.Errorf("failed to create os build dir: %w", err)
		} else {
			dockerfileContent := `FROM ubuntu:24.04
RUN apt-get update && apt-get install -y ca-certificates curl && rm -rf /var/lib/apt/lists/*
`
			if err = os.WriteFile(filepath.Join(cloneDir, dockerfilePath), []byte(dockerfileContent), 0o644); err != nil {
				err = fmt.Errorf("failed to write os base Dockerfile: %w", err)
			}
		}
	} else {
		appendLog("Cloning repository...")
		cloneDir, err = cloneRepo(req.RepoFullName, req.CommitSHA)
	}

	if err != nil {
		appendLog("Setup failed: " + err.Error())
		cleanup(req.RepoFullName)
		return BuildResult{
			Success:   false,
			ImageName: req.ImageName,
			Logs:      strings.Join(logs, "\n"),
			Error:     err.Error(),
		}
	}

	// Apply patches for specific repositories
	if strings.Contains(strings.ToLower(req.RepoFullName), "skyclaw") {
		appendLog("Skyclaw detected. Patching Dockerfile to use Rust 1.85 and fix ENV warning...")
		dPath := filepath.Join(cloneDir, dockerfilePath)
		if content, err := os.ReadFile(dPath); err == nil {
			patched := strings.Replace(string(content), "FROM rust:1.82", "FROM rust:1.85", 1)
			// Replace ENV TELEGRAM_BOT_TOKEN="" with ARG TELEGRAM_BOT_TOKEN="" to avoid warnings
			patched = strings.Replace(patched, "ENV TELEGRAM_BOT_TOKEN=\"\"", "ARG TELEGRAM_BOT_TOKEN=\"\"", 1)
			if err := os.WriteFile(dPath, []byte(patched), 0o644); err != nil {
				appendLog(fmt.Sprintf("Warning: failed to write patched Dockerfile: %v", err))
			} else {
				appendLog("Successfully patched Skyclaw Dockerfile")
			}
		} else {
			appendLog(fmt.Sprintf("Warning: failed to read Dockerfile for patching: %v", err))
		}
	}

	appendLog("Build environment ready")

	// Step 2: Build
	appendLog("Building Docker image...")
	if err := buildImage(cloneDir, req.ImageName, dockerfilePath, req.VersionTag); err != nil {
		appendLog("Build failed: " + err.Error())
		cleanup(req.RepoFullName)
		return BuildResult{
			Success:   false,
			ImageName: req.ImageName,
			Logs:      strings.Join(logs, "\n"),
			Error:     err.Error(),
		}
	}
	appendLog("Image built successfully")

	// Step 3: Push
	appendLog("Pushing to registry...")
	pushedImage, err := pushImage(req.ImageName, req.VersionTag)
	if err != nil {
		appendLog("Push failed: " + err.Error())
		cleanup(req.RepoFullName)
		return BuildResult{
			Success:   false,
			ImageName: req.ImageName,
			Logs:      strings.Join(logs, "\n"),
			Error:     err.Error(),
		}
	}
	appendLog("Image pushed successfully: " + pushedImage)

	cleanup(req.RepoFullName)
	duration := time.Since(start).Seconds()
	appendLog(fmt.Sprintf("Build completed in %.1fs", duration))

	return BuildResult{
		Success:   true,
		ImageName: pushedImage,
		Logs:      strings.Join(logs, "\n"),
	}
}
