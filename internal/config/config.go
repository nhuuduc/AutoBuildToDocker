package config

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type TelegramConfig struct {
	BotToken string
}

type DockerConfig struct {
	Registry string
	Username string
	Password string
}

type GitHubConfig struct {
	Token string
}

type DbConfig struct {
	Path string
}

type SchedulerConfig struct {
	DefaultIntervalMinutes int
}

type ServerConfig struct {
	Port int
}

type Config struct {
	Telegram  TelegramConfig
	Docker    DockerConfig
	GitHub    GitHubConfig
	DB        DbConfig
	Scheduler SchedulerConfig
	Server    ServerConfig
}

var App *Config

// Load loads config from .env file and environment variables.
func Load() *Config {
	// Load .env (ignore error if file doesn't exist)
	_ = godotenv.Load()

	intervalMinutes := 60
	if v := os.Getenv("CHECK_INTERVAL_MINUTES"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			intervalMinutes = parsed
		}
	}

	port := 3000
	if v := os.Getenv("PORT"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			port = parsed
		}
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./data/app.db"
	}

	registry := os.Getenv("DOCKER_REGISTRY")
	if registry == "" {
		registry = "docker.io"
	}

	App = &Config{
		Telegram: TelegramConfig{
			BotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		},
		Docker: DockerConfig{
			Registry: registry,
			Username: os.Getenv("DOCKER_USERNAME"),
			Password: os.Getenv("DOCKER_PASSWORD"),
		},
		GitHub: GitHubConfig{
			Token: os.Getenv("GITHUB_TOKEN"),
		},
		DB: DbConfig{
			Path: dbPath,
		},
		Scheduler: SchedulerConfig{
			DefaultIntervalMinutes: intervalMinutes,
		},
		Server: ServerConfig{
			Port: port,
		},
	}

	if App.Telegram.BotToken == "" {
		log.Println("Warning: TELEGRAM_BOT_TOKEN is not set in environment")
	}

	return App
}
