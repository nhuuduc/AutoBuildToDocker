package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

var DB *sql.DB

// Init initializes the SQLite database and creates tables.
func Init(dbPath string) (*sql.DB, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := createTables(db); err != nil {
		return nil, fmt.Errorf("create tables: %w", err)
	}

	DB = db
	log.Printf("Database initialized at: %s", dbPath)
	return db, nil
}

// Close closes the database connection.
func Close() {
	if DB != nil {
		_ = DB.Close()
		log.Println("Database connection closed")
	}
}

func createTables(db *sql.DB) error {
	schema := `
	-- Users table: stores Telegram users
	CREATE TABLE IF NOT EXISTS users (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		telegram_id BIGINT UNIQUE NOT NULL,
		username   TEXT,
		first_name TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	-- Repositories table: stores GitHub repositories to monitor
	CREATE TABLE IF NOT EXISTS repositories (
		id                     INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id                INTEGER NOT NULL,
		owner                  TEXT NOT NULL,
		repo                   TEXT NOT NULL,
		branch                 TEXT DEFAULT 'main',
		dockerfile_path        TEXT DEFAULT 'Dockerfile',
		image_name             TEXT NOT NULL,
		registry_url           TEXT DEFAULT 'docker.io',
		last_commit_sha        TEXT,
		last_release_tag       TEXT,
		check_interval_minutes INTEGER DEFAULT 60,
		is_active              BOOLEAN DEFAULT 1,
		created_at             DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at             DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
		UNIQUE(user_id, owner, repo)
	);

	-- Builds table: stores build history
	CREATE TABLE IF NOT EXISTS builds (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		repo_id      INTEGER NOT NULL,
		commit_sha   TEXT,
		build_status TEXT CHECK(build_status IN ('pending','building','success','failed')),
		docker_image TEXT,
		started_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
		completed_at DATETIME,
		logs         TEXT,
		FOREIGN KEY (repo_id) REFERENCES repositories(id) ON DELETE CASCADE
	);

	-- Confirmations table: stores pending build confirmations
	CREATE TABLE IF NOT EXISTS confirmations (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		repo_id     INTEGER NOT NULL,
		update_type TEXT CHECK(update_type IN ('commit','release')),
		update_sha  TEXT,
		message_id  INTEGER,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at  DATETIME,
		FOREIGN KEY (repo_id) REFERENCES repositories(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_repositories_user_id    ON repositories(user_id);
	CREATE INDEX IF NOT EXISTS idx_builds_repo_id          ON builds(repo_id);
	CREATE INDEX IF NOT EXISTS idx_confirmations_repo_id   ON confirmations(repo_id);
	CREATE INDEX IF NOT EXISTS idx_confirmations_expires_at ON confirmations(expires_at);
	`
	_, err := db.Exec(schema)
	return err
}
