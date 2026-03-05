package services

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Feature describes an optional package to add on top of the built image.
type Feature struct {
	Key     string
	Label   string
	Emoji   string
	RunCmds []string // shell commands to RUN in the addon Dockerfile layer
}

// AvailableFeatures is the ordered list of selectable features.
var AvailableFeatures = []Feature{
	{
		Key:   "python3",
		Label: "Python3",
		Emoji: "🐍",
		RunCmds: []string{
			"apk add --no-cache python3 py3-pip",
		},
	},
	{
		Key:   "playwright",
		Label: "Playwright",
		Emoji: "🎭",
		RunCmds: []string{
			"apk add --no-cache python3 py3-pip chromium chromium-chromedriver",
			"pip3 install --break-system-packages playwright",
			"playwright install chromium",
		},
	},
	{
		Key:   "nodejs",
		Label: "Node.js",
		Emoji: "💚",
		RunCmds: []string{
			"apk add --no-cache nodejs npm",
		},
	},
	{
		Key:   "ffmpeg",
		Label: "FFmpeg",
		Emoji: "🎬",
		RunCmds: []string{
			"apk add --no-cache ffmpeg",
		},
	},
}

// FeatureByKey returns a Feature by its key, or nil if not found.
func FeatureByKey(key string) *Feature {
	for i := range AvailableFeatures {
		if AvailableFeatures[i].Key == key {
			return &AvailableFeatures[i]
		}
	}
	return nil
}

// BuildAddonLayer builds an extra Docker layer on top of imageName:latest
// that installs the selected features. The resulting image replaces imageName:latest.
// Returns nil if features is empty.
func BuildAddonLayer(imageName string, features []string) error {
	if len(features) == 0 {
		return nil
	}

	// Build list of RUN commands from selected features
	var runLines []string
	for _, key := range features {
		f := FeatureByKey(key)
		if f == nil {
			log.Printf("[Features] Unknown feature key: %s — skipping", key)
			continue
		}
		for _, cmd := range f.RunCmds {
			runLines = append(runLines, "RUN "+cmd)
		}
	}
	if len(runLines) == 0 {
		return nil
	}

	fullImage := getImageWithTag(imageName, "latest")

	// Write temp Dockerfile
	addonDir, err := os.MkdirTemp("", "addon-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir for addon build: %w", err)
	}
	defer os.RemoveAll(addonDir)

	dockerfileContent := fmt.Sprintf("FROM %s\n%s\n", fullImage, strings.Join(runLines, "\n"))
	dockerfilePath := filepath.Join(addonDir, "Dockerfile.addon")
	if err := os.WriteFile(dockerfilePath, []byte(dockerfileContent), 0o644); err != nil {
		return fmt.Errorf("failed to write addon Dockerfile: %w", err)
	}

	log.Printf("[Features] Building addon layer for %s with: %v", fullImage, features)
	log.Printf("[Features] Addon Dockerfile:\n%s", dockerfileContent)

	prefix := fmt.Sprintf("[Features/build %s]", imageName)
	if err := runLogged(prefix,
		"docker", "build", "--progress=plain",
		"-t", fullImage,
		"-f", dockerfilePath,
		addonDir,
	); err != nil {
		return fmt.Errorf("addon layer build failed: %w", err)
	}

	log.Printf("[Features] ✅ Addon layer built: %s (features: %s)", fullImage, strings.Join(features, ", "))
	return nil
}
