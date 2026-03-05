package db

import (
	"database/sql"
	"fmt"
	"time"
)

// ─── Models ───────────────────────────────────────────────────────────────────

type User struct {
	ID         int64
	TelegramID int64
	Username   sql.NullString
	FirstName  sql.NullString
	CreatedAt  string
}

type Repository struct {
	ID                   int64
	UserID               int64
	Owner                string
	Repo                 string
	Branch               string
	DockerfilePath       string
	ImageName            string
	RegistryURL          string
	LastCommitSHA        sql.NullString
	LastReleaseTag       sql.NullString
	CheckIntervalMinutes int
	IsActive             bool
	CreatedAt            string
	UpdatedAt            string
}

type Build struct {
	ID          int64
	RepoID      int64
	CommitSHA   sql.NullString
	BuildStatus string
	DockerImage sql.NullString
	StartedAt   string
	CompletedAt sql.NullString
	Logs        sql.NullString
}

type Confirmation struct {
	ID         int64
	RepoID     int64
	UpdateType string
	UpdateSHA  string
	MessageID  sql.NullInt64
	CreatedAt  string
	ExpiresAt  string
}

// ─── User Queries ─────────────────────────────────────────────────────────────

func FindUserByTelegramID(telegramID int64) (*User, error) {
	row := DB.QueryRow(`SELECT id, telegram_id, username, first_name, created_at FROM users WHERE telegram_id = ?`, telegramID)
	return scanUser(row)
}

func UpsertUser(telegramID int64, username, firstName string) error {
	_, err := DB.Exec(`
		INSERT INTO users (telegram_id, username, first_name)
		VALUES (?, ?, ?)
		ON CONFLICT(telegram_id) DO UPDATE SET
			username   = excluded.username,
			first_name = excluded.first_name
	`, telegramID, nullString(username), nullString(firstName))
	return err
}

func GetAllUsers() ([]User, error) {
	rows, err := DB.Query(`SELECT id, telegram_id, username, first_name, created_at FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u := User{}
		if err := rows.Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ─── Repository Queries ───────────────────────────────────────────────────────

func FindRepoByID(id int64) (*Repository, error) {
	row := DB.QueryRow(`SELECT * FROM repositories WHERE id = ?`, id)
	return scanRepo(row)
}

func FindRepoByUserAndFullName(userID int64, owner, repo string) (*Repository, error) {
	row := DB.QueryRow(`SELECT * FROM repositories WHERE user_id = ? AND owner = ? AND repo = ?`, userID, owner, repo)
	return scanRepo(row)
}

func FindReposByUser(userID int64) ([]Repository, error) {
	rows, err := DB.Query(`SELECT * FROM repositories WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRepos(rows)
}

func FindAllActiveRepos() ([]Repository, error) {
	rows, err := DB.Query(`SELECT * FROM repositories WHERE is_active = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRepos(rows)
}

func CreateRepo(userID int64, owner, repo, branch, dockerfilePath, imageName, registryURL string, intervalMinutes int) (int64, error) {
	res, err := DB.Exec(`
		INSERT INTO repositories (user_id, owner, repo, branch, dockerfile_path, image_name, registry_url, check_interval_minutes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, userID, owner, repo, branch, dockerfilePath, imageName, registryURL, intervalMinutes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateRepoLastCommit(id int64, commitSHA string) error {
	_, err := DB.Exec(`UPDATE repositories SET last_commit_sha = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, commitSHA, id)
	return err
}

func UpdateRepoLastRelease(id int64, releaseTag string) error {
	_, err := DB.Exec(`UPDATE repositories SET last_release_tag = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, releaseTag, id)
	return err
}

func SetRepoActive(id int64, isActive bool) error {
	v := 0
	if isActive {
		v = 1
	}
	_, err := DB.Exec(`UPDATE repositories SET is_active = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, v, id)
	return err
}

func DeleteRepo(id int64) error {
	_, err := DB.Exec(`DELETE FROM repositories WHERE id = ?`, id)
	return err
}

// ─── Build Queries ────────────────────────────────────────────────────────────

func FindBuildsByRepo(repoID int64, limit int) ([]Build, error) {
	rows, err := DB.Query(`
		SELECT id, repo_id, commit_sha, build_status, docker_image, started_at, completed_at, logs
		FROM builds WHERE repo_id = ? ORDER BY started_at DESC LIMIT ?
	`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBuilds(rows)
}

func FindAllBuildsRecent(limit int) ([]Build, error) {
	rows, err := DB.Query(`
		SELECT id, repo_id, commit_sha, build_status, docker_image, started_at, completed_at, logs
		FROM builds ORDER BY started_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBuilds(rows)
}

func CreateBuild(repoID int64, commitSHA, dockerImage string) (int64, error) {
	res, err := DB.Exec(`
		INSERT INTO builds (repo_id, commit_sha, build_status, docker_image)
		VALUES (?, ?, 'pending', ?)
	`, repoID, nullString(commitSHA), nullString(dockerImage))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateBuildStatus(id int64, status, logs string) error {
	if status == "success" || status == "failed" {
		_, err := DB.Exec(`
			UPDATE builds SET build_status = ?, logs = ?, completed_at = CURRENT_TIMESTAMP WHERE id = ?
		`, status, nullString(logs), id)
		return err
	}
	_, err := DB.Exec(`UPDATE builds SET build_status = ?, logs = ? WHERE id = ?`, status, nullString(logs), id)
	return err
}

// ─── Confirmation Queries ─────────────────────────────────────────────────────

func FindConfirmationByRepo(repoID int64) (*Confirmation, error) {
	row := DB.QueryRow(`
		SELECT id, repo_id, update_type, update_sha, message_id, created_at, expires_at
		FROM confirmations WHERE repo_id = ? AND expires_at > CURRENT_TIMESTAMP
		ORDER BY created_at DESC LIMIT 1
	`, repoID)
	return scanConfirmation(row)
}

func CreateConfirmation(repoID int64, updateType, updateSHA string, messageID int64) error {
	expiresAt := time.Now().Add(30 * time.Minute).UTC().Format("2006-01-02 15:04:05")
	_, err := DB.Exec(`
		INSERT INTO confirmations (repo_id, update_type, update_sha, message_id, expires_at)
		VALUES (?, ?, ?, ?, ?)
	`, repoID, updateType, updateSHA, nullInt64(messageID), expiresAt)
	return err
}

func DeleteConfirmation(id int64) error {
	_, err := DB.Exec(`DELETE FROM confirmations WHERE id = ?`, id)
	return err
}

func DeleteConfirmationsByRepo(repoID int64) error {
	_, err := DB.Exec(`DELETE FROM confirmations WHERE repo_id = ?`, repoID)
	return err
}

func DeleteExpiredConfirmations() error {
	_, err := DB.Exec(`DELETE FROM confirmations WHERE expires_at <= CURRENT_TIMESTAMP`)
	return err
}

// ─── Scanner helpers ──────────────────────────────────────────────────────────

func scanUser(row *sql.Row) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func scanRepo(row *sql.Row) (*Repository, error) {
	r := &Repository{}
	err := row.Scan(
		&r.ID, &r.UserID, &r.Owner, &r.Repo, &r.Branch,
		&r.DockerfilePath, &r.ImageName, &r.RegistryURL,
		&r.LastCommitSHA, &r.LastReleaseTag, &r.CheckIntervalMinutes,
		&r.IsActive, &r.CreatedAt, &r.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func scanRepos(rows *sql.Rows) ([]Repository, error) {
	var repos []Repository
	for rows.Next() {
		r := Repository{}
		if err := rows.Scan(
			&r.ID, &r.UserID, &r.Owner, &r.Repo, &r.Branch,
			&r.DockerfilePath, &r.ImageName, &r.RegistryURL,
			&r.LastCommitSHA, &r.LastReleaseTag, &r.CheckIntervalMinutes,
			&r.IsActive, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

func scanBuilds(rows *sql.Rows) ([]Build, error) {
	var builds []Build
	for rows.Next() {
		b := Build{}
		if err := rows.Scan(&b.ID, &b.RepoID, &b.CommitSHA, &b.BuildStatus, &b.DockerImage, &b.StartedAt, &b.CompletedAt, &b.Logs); err != nil {
			return nil, err
		}
		builds = append(builds, b)
	}
	return builds, rows.Err()
}

func scanConfirmation(row *sql.Row) (*Confirmation, error) {
	c := &Confirmation{}
	err := row.Scan(&c.ID, &c.RepoID, &c.UpdateType, &c.UpdateSHA, &c.MessageID, &c.CreatedAt, &c.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// ─── Null helpers ─────────────────────────────────────────────────────────────

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullInt64(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

// GetUserByID is a helper to find user by db id scanning entire users table.
func GetUserByID(id int64) (*User, error) {
	row := DB.QueryRow(`SELECT id, telegram_id, username, first_name, created_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

// FindLatestBuildByRepo returns the most recent build for a repo.
func FindLatestBuildByRepo(repoID int64) (*Build, error) {
	builds, err := FindBuildsByRepo(repoID, 1)
	if err != nil || len(builds) == 0 {
		return nil, fmt.Errorf("no builds found: %w", err)
	}
	b := builds[0]
	return &b, nil
}
