package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/nhd/autobuildtodocker/internal/api"
	"github.com/nhd/autobuildtodocker/internal/bot"
	"github.com/nhd/autobuildtodocker/internal/config"
	"github.com/nhd/autobuildtodocker/internal/db"
	"github.com/nhd/autobuildtodocker/internal/services"
)

func main() {
	log.Println("Starting CI/CD Telegram Bot (Go edition)...")

	// Load config
	cfg := config.Load()

	// Init database
	if _, err := db.Init(cfg.DB.Path); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// Setup HTTP server
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: mux,
	}

	// Start HTTP server in background
	go func() {
		log.Printf("HTTP server listening on :%d", cfg.Server.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Start Telegram bot
	var teleBot interface{ Stop() }
	if cfg.Telegram.BotToken != "" {
		b, err := bot.New()
		if err != nil {
			log.Printf("Warning: Failed to create Telegram bot: %v (HTTP server and scheduler will still run)", err)
		} else if b != nil {
			go b.Start()
			teleBot = b
			log.Println("Telegram bot started")
		}
	} else {
		log.Println("TELEGRAM_BOT_TOKEN not set, bot will not start")
	}

	// Start scheduler
	interval := cfg.Scheduler.DefaultIntervalMinutes
	if interval <= 0 {
		interval = 5
	}
	services.StartScheduler(interval)
	defer services.StopScheduler()

	// Graceful shutdown on SIGINT/SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	log.Println("Shutting down gracefully...")

	if teleBot != nil {
		teleBot.Stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	log.Println("Shutdown complete")
}
