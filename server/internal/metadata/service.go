package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/diskwave/server/internal/pathutil"
	"github.com/redis/go-redis/v9"
	_ "github.com/lib/pq"
)

type FileType int

const (
	TypeFile FileType = iota
	TypeDirectory
	TypeSymlink
)

type Entry struct {
	Name  string   `json:"name"`
	Type  FileType `json:"type"`
	Size  int64    `json:"size"`
	Mtime int64    `json:"mtime"`
	Mode  uint32   `json:"mode"`
}

type Service struct {
	db    *sql.DB
	redis *redis.Client
}

const cacheTTL = 5 * time.Second

func NewService(db *sql.DB, rdb *redis.Client) (*Service, error) {
	s := &Service{db: db, redis: rdb}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	// Ensure root directory exists
	_ = s.Mkdir(context.Background(), "/", 0755)
	return s, nil
}

func (s *Service) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS inodes (
			id         BIGSERIAL PRIMARY KEY,
			parent_id  BIGINT REFERENCES inodes(id) ON DELETE CASCADE,
			name       TEXT NOT NULL,
			path       TEXT NOT NULL UNIQUE,
			type       INT NOT NULL DEFAULT 0,
			size       BIGINT NOT NULL DEFAULT 0,
			mode       INT NOT NULL DEFAULT 420,
			mtime      BIGINT NOT NULL,
			created_at BIGINT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS inodes_parent ON inodes(parent_id);
		CREATE INDEX IF NOT EXISTS inodes_path ON inodes(path);
	`)
	return err
}

func (s *Service) Stat(ctx context.Context, path string) (*Entry, error) {
	if err := pathutil.ValidatePath(path); err != nil {
		return nil, err
	}
	// Try Redis cache first
	cacheKey := "stat:" + path
	if cached, err := s.redis.Get(ctx, cacheKey).Bytes(); err == nil {
		var e Entry
		if json.Unmarshal(cached, &e) == nil {
			return &e, nil
		}
	}

	row := s.db.QueryRowContext(ctx,
		`SELECT name, type, size, mode, mtime FROM inodes WHERE path = $1`, path)

	var e Entry
	if err := row.Scan(&e.Name, &e.Type, &e.Size, &e.Mode, &e.Mtime); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("not found: %s", path)
		}
		return nil, err
	}

	if data, err := json.Marshal(&e); err == nil {
		_ = s.redis.Set(ctx, cacheKey, data, cacheTTL).Err()
	}
	return &e, nil
}

func (s *Service) ReadDir(ctx context.Context, path string) ([]*Entry, error) {
	if err := pathutil.ValidatePath(path); err != nil {
		return nil, err
	}
	cacheKey := "readdir:" + path

	if cached, err := s.redis.Get(ctx, cacheKey).Bytes(); err == nil {
		var entries []*Entry
		if json.Unmarshal(cached, &entries) == nil {
			return entries, nil
		}
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT name, type, size, mode, mtime FROM inodes WHERE parent_id = (
			SELECT id FROM inodes WHERE path = $1
		) ORDER BY name`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Name, &e.Type, &e.Size, &e.Mode, &e.Mtime); err != nil {
			return nil, err
		}
		entries = append(entries, &e)
	}

	if data, err := json.Marshal(entries); err == nil {
		_ = s.redis.Set(ctx, cacheKey, data, cacheTTL).Err()
	}
	return entries, nil
}

func (s *Service) Mkdir(ctx context.Context, path string, mode uint32) error {
	if err := pathutil.ValidatePath(path); err != nil {
		return err
	}
	now := time.Now().Unix()
	name := filepath.Base(path)
	if path == "/" {
		name = "/"
	}

	var parentID *int64
	if path != "/" {
		parent := filepath.Dir(path)
		var pid int64
		err := s.db.QueryRowContext(ctx, `SELECT id FROM inodes WHERE path = $1`, parent).Scan(&pid)
		if err != nil {
			return fmt.Errorf("parent not found: %s", parent)
		}
		parentID = &pid
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO inodes (parent_id, name, path, type, mode, mtime, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (path) DO NOTHING`,
		parentID, name, path, TypeDirectory, mode, now, now)
	if err != nil {
		return err
	}
	s.invalidateCache(ctx, filepath.Dir(path))
	return nil
}

func (s *Service) Mknod(ctx context.Context, path string, mode uint32) error {
	if err := pathutil.ValidatePath(path); err != nil {
		return err
	}
	now := time.Now().Unix()
	name := filepath.Base(path)
	parent := filepath.Dir(path)

	var parentID int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM inodes WHERE path = $1`, parent).Scan(&parentID)
	if err != nil {
		return fmt.Errorf("parent not found: %s", parent)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO inodes (parent_id, name, path, type, mode, size, mtime, created_at)
		 VALUES ($1, $2, $3, $4, $5, 0, $6, $7)
		 ON CONFLICT (path) DO NOTHING`,
		parentID, name, path, TypeFile, mode, now, now)
	if err != nil {
		return err
	}
	s.invalidateCache(ctx, parent)
	return nil
}

func (s *Service) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := pathutil.ValidatePath(oldPath); err != nil {
		return err
	}
	if err := pathutil.ValidatePath(newPath); err != nil {
		return err
	}
	// Update path and name; also update all children paths
	rows, err := s.db.QueryContext(ctx,
		`SELECT path FROM inodes WHERE path = $1 OR path LIKE $2 ORDER BY path`,
		oldPath, oldPath+"/%")
	if err != nil {
		return err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return err
		}
		paths = append(paths, p)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	for _, p := range paths {
		newP := newPath + strings.TrimPrefix(p, oldPath)
		newName := filepath.Base(newP)

		var newParentID *int64
		if newP != "/" {
			var pid int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM inodes WHERE path = $1`, filepath.Dir(newP)).Scan(&pid)
			if err == nil {
				newParentID = &pid
			}
		}

		_, err = tx.ExecContext(ctx,
			`UPDATE inodes SET path = $1, name = $2, parent_id = $3, mtime = $4 WHERE path = $5`,
			newP, newName, newParentID, now, p)
		if err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	s.invalidateCache(ctx, filepath.Dir(oldPath))
	s.invalidateCache(ctx, filepath.Dir(newPath))
	s.invalidateCache(ctx, oldPath)
	return nil
}

func (s *Service) Delete(ctx context.Context, path string) error {
	if err := pathutil.ValidatePath(path); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM inodes WHERE path = $1`, path)
	if err != nil {
		return err
	}
	s.invalidateCache(ctx, filepath.Dir(path))
	s.invalidateCache(ctx, path)
	return nil
}

func (s *Service) SetSize(ctx context.Context, path string, size int64) error {
	if err := pathutil.ValidatePath(path); err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`UPDATE inodes SET size = $1, mtime = $2 WHERE path = $3`,
		size, now, path)
	if err != nil {
		return err
	}
	s.invalidateCache(ctx, path)
	return nil
}

func (s *Service) invalidateCache(ctx context.Context, path string) {
	_ = s.redis.Del(ctx, "stat:"+path, "readdir:"+path).Err()
}
