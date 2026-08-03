package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Adapter interface {
	Put(ctx context.Context, hash string, data []byte) error
	Get(ctx context.Context, hash string) ([]byte, error)
	Exists(ctx context.Context, hash string) (bool, error)
	Delete(ctx context.Context, hash string) error
}

// MinIOAdapter — S3-compatible storage (MinIO, AWS S3, Backblaze B2, Hetzner)
type MinIOAdapter struct {
	client *minio.Client
	bucket string
}

func NewMinIOAdapter(endpoint, accessKey, secretKey, bucket string, useSSL bool) (*MinIOAdapter, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minio client: %w", err)
	}

	ctx := context.Background()
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("bucket check: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("make bucket: %w", err)
		}
	}

	return &MinIOAdapter{client: client, bucket: bucket}, nil
}

func (a *MinIOAdapter) Put(ctx context.Context, hash string, data []byte) error {
	pr, pw := io.Pipe()
	go func() {
		_, err := pw.Write(data)
		pw.CloseWithError(err)
	}()
	_, err := a.client.PutObject(ctx, a.bucket, hash, pr, int64(len(data)), minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	return err
}

func (a *MinIOAdapter) Get(ctx context.Context, hash string) ([]byte, error) {
	obj, err := a.client.GetObject(ctx, a.bucket, hash, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

func (a *MinIOAdapter) Exists(ctx context.Context, hash string) (bool, error) {
	_, err := a.client.StatObject(ctx, a.bucket, hash, minio.StatObjectOptions{})
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "NoSuchKey" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (a *MinIOAdapter) Delete(ctx context.Context, hash string) error {
	return a.client.RemoveObject(ctx, a.bucket, hash, minio.RemoveObjectOptions{})
}

// LocalAdapter — development/testing without MinIO
type LocalAdapter struct {
	dir string
}

func NewLocalAdapter(dir string) (*LocalAdapter, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &LocalAdapter{dir: dir}, nil
}

func (a *LocalAdapter) path(hash string) string {
	return filepath.Join(a.dir, hash[:2], hash)
}

func (a *LocalAdapter) Put(ctx context.Context, hash string, data []byte) error {
	p := a.path(hash)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0644)
}

func (a *LocalAdapter) Get(ctx context.Context, hash string) ([]byte, error) {
	return os.ReadFile(a.path(hash))
}

func (a *LocalAdapter) Exists(ctx context.Context, hash string) (bool, error) {
	_, err := os.Stat(a.path(hash))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (a *LocalAdapter) Delete(ctx context.Context, hash string) error {
	return os.Remove(a.path(hash))
}