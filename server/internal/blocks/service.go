package blocks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/diskwave/server/internal/storage"
)

const blockSize = 4 * 1024 * 1024 // 4MB

type Service struct {
	storage storage.Adapter
	// Local staging dir for file data before block split
	stagingDir string
}

func NewService(adapter storage.Adapter, stagingDir string) (*Service, error) {
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return nil, err
	}
	return &Service{storage: adapter, stagingDir: stagingDir}, nil
}

// Read returns a slice of the file's data at [offset, offset+size).
// Files are stored as flat byte streams reconstructed from blocks.
// For simplicity in MVP: files are stored as single objects keyed by path hash.
func (s *Service) Read(ctx context.Context, path string, offset, size int64) ([]byte, error) {
	key := pathKey(path)

	data, err := s.storage.Get(ctx, key)
	if err != nil {
		// Empty file
		return []byte{}, nil
	}

	if offset >= int64(len(data)) {
		return []byte{}, nil
	}

	end := offset + size
	if end > int64(len(data)) {
		end = int64(len(data))
	}

	return data[offset:end], nil
}

// Write writes data at offset into the file, extending if necessary.
func (s *Service) Write(ctx context.Context, path string, offset int64, data []byte) (int64, error) {
	key := pathKey(path)

	existing, err := s.storage.Get(ctx, key)
	if err != nil {
		existing = []byte{}
	}

	end := offset + int64(len(data))
	if end > int64(len(existing)) {
		extended := make([]byte, end)
		copy(extended, existing)
		existing = extended
	}

	copy(existing[offset:], data)

	if err := s.storage.Put(ctx, key, existing); err != nil {
		return 0, fmt.Errorf("storage put: %w", err)
	}

	return int64(len(data)), nil
}

// Truncate resizes the file to the given size.
func (s *Service) Truncate(ctx context.Context, path string, size int64) error {
	key := pathKey(path)

	existing, err := s.storage.Get(ctx, key)
	if err != nil {
		existing = []byte{}
	}

	if int64(len(existing)) == size {
		return nil
	}

	resized := make([]byte, size)
	copy(resized, existing)

	return s.storage.Put(ctx, key, resized)
}

// PutAll replaces the entire file content in a single storage call.
// Used by webdav/blockFile.Close() to flush the in-memory buffer once.
func (s *Service) PutAll(ctx context.Context, path string, data []byte) error {
	return s.storage.Put(ctx, pathKey(path), data)
}

// Rename moves block data from oldPath key to newPath key.
func (s *Service) Rename(ctx context.Context, oldPath, newPath string) error {
	data, err := s.storage.Get(ctx, pathKey(oldPath))
	if err != nil {
		// No block data for this path (e.g. empty file or directory) — not an error
		return nil
	}
	if err := s.storage.Put(ctx, pathKey(newPath), data); err != nil {
		return fmt.Errorf("block rename put: %w", err)
	}
	_ = s.storage.Delete(ctx, pathKey(oldPath))
	return nil
}

// Delete removes all blocks for a file.
func (s *Service) Delete(ctx context.Context, path string) error {
	key := pathKey(path)
	return s.storage.Delete(ctx, key)
}

// Size returns current byte length of file data.
func (s *Service) Size(ctx context.Context, path string) (int64, error) {
	key := pathKey(path)
	data, err := s.storage.Get(ctx, key)
	if err != nil {
		return 0, nil
	}
	return int64(len(data)), nil
}

// BlockExists checks if a content-addressed block exists (for future dedup).
func (s *Service) BlockExists(ctx context.Context, hash string) (bool, error) {
	return s.storage.Exists(ctx, hash)
}

// ContentHash computes SHA-256 of data.
func ContentHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// pathKey converts a file path to a stable storage key.
func pathKey(path string) string {
	// Normalize and use as storage key prefix to avoid clashes
	clean := strings.ReplaceAll(strings.TrimPrefix(filepath.Clean(path), "/"), "/", "_")
	h := sha256.Sum256([]byte(path))
	return "file_" + clean + "_" + hex.EncodeToString(h[:8])
}

// WriteToStaging writes raw data to a temp staging file (for large uploads).
func (s *Service) WriteToStaging(path string, r io.Reader) (string, error) {
	tmpFile := filepath.Join(s.stagingDir, hex.EncodeToString([]byte(path)))
	f, err := os.Create(tmpFile)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return "", err
	}
	return tmpFile, nil
}