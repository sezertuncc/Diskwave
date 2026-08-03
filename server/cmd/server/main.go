package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/diskwave/server/internal/auth"
	"github.com/diskwave/server/internal/blocks"
	"github.com/diskwave/server/internal/metadata"
	"github.com/diskwave/server/internal/mgmt"
	dsquic "github.com/diskwave/server/internal/quic"
	"github.com/diskwave/server/internal/storage"
	dstcp "github.com/diskwave/server/internal/tcp"
	"github.com/redis/go-redis/v9"
	_ "github.com/lib/pq"
)

func main() {
	cfg := loadConfig()

	// --- Storage ---
	var storageAdapter storage.Adapter
	var err error

	if cfg.storageType == "local" {
		storageAdapter, err = storage.NewLocalAdapter(cfg.localStorageDir)
		if err != nil {
			log.Fatalf("local storage: %v", err)
		}
		log.Printf("[storage] Using local filesystem: %s", cfg.localStorageDir)
	} else {
		storageAdapter, err = storage.NewMinIOAdapter(
			cfg.minioEndpoint,
			cfg.minioAccessKey,
			cfg.minioSecretKey,
			cfg.minioBucket,
			cfg.minioUseSSL,
		)
		if err != nil {
			log.Fatalf("minio storage: %v", err)
		}
		log.Printf("[storage] Using MinIO: %s / %s", cfg.minioEndpoint, cfg.minioBucket)
	}

	// --- PostgreSQL ---
	db, err := sql.Open("postgres", cfg.postgresURL)
	if err != nil {
		log.Fatalf("postgres open: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("postgres ping: %v", err)
	}
	log.Printf("[db] Connected to PostgreSQL")

	// --- Redis ---
	rdb := redis.NewClient(&redis.Options{Addr: cfg.redisAddr})
	log.Printf("[cache] Connected to Redis: %s", cfg.redisAddr)

	// --- Services ---
	authMgr, err := auth.NewManager()
	if err != nil {
		log.Fatalf("auth manager: %v", err)
	}

	metaSvc, err := metadata.NewService(db, rdb)
	if err != nil {
		log.Fatalf("metadata service: %v", err)
	}

	blockSvc, err := blocks.NewService(storageAdapter, cfg.stagingDir)
	if err != nil {
		log.Fatalf("blocks service: %v", err)
	}

	// --- Print pairing code ---
	code := authMgr.GetCurrentCode()
	fmt.Printf("\n")
	fmt.Printf("╔══════════════════════════════════╗\n")
	fmt.Printf("║   Diskwave Server Ready          ║\n")
	fmt.Printf("║                                  ║\n")
	fmt.Printf("║   Pairing code: %-6s           ║\n", code)
	fmt.Printf("║   QUIC port:    %-5s            ║\n", cfg.quicPort)
	fmt.Printf("║   TCP port:     %-5s            ║\n", cfg.tcpPort)
	fmt.Printf("║   SMB port:     445              ║\n")
	fmt.Printf("║                                  ║\n")
	fmt.Printf("╚══════════════════════════════════╝\n")
	fmt.Printf("\n")

	// Rotate code in background
	stop := make(chan struct{})
	go authMgr.StartRotation(stop)

	// --- Management API (localhost only) ---
	mgmtAPI := mgmt.NewAPI(authMgr)
	go func() {
		if err := mgmtAPI.ListenAndServe("0.0.0.0:" + cfg.mgmtPort); err != nil {
			log.Printf("[mgmt] %v", err)
		}
	}()

	// --- TCP server (for Swift client pairing) ---
	tcpHandler := dstcp.NewHandler(authMgr, metaSvc, blockSvc)
	tcpAddr := fmt.Sprintf("0.0.0.0:%s", cfg.tcpPort)
	go func() {
		if err := tcpHandler.ListenAndServe(tcpAddr); err != nil {
			log.Fatalf("[tcp] %v", err)
		}
	}()

	// --- QUIC server ---
	handler := dsquic.NewHandler(authMgr, metaSvc, blockSvc)
	addr := fmt.Sprintf("0.0.0.0:%s", cfg.quicPort)
	log.Fatalf("[quic] %v", handler.ListenAndServe(addr))
}

type config struct {
	quicPort        string
	tcpPort         string
	mgmtPort        string
	postgresURL     string
	redisAddr       string
	storageType     string
	localStorageDir string
	minioEndpoint   string
	minioAccessKey  string
	minioSecretKey  string
	minioBucket     string
	minioUseSSL     bool
	stagingDir      string
}

func loadConfig() config {
	return config{
		quicPort:        envOr("QUIC_PORT", "7878"),
		tcpPort:         envOr("TCP_PORT", "7879"),
		mgmtPort:        envOr("MGMT_PORT", "7880"),
		postgresURL:     envOr("POSTGRES_URL", "postgres://diskwave:diskwave@localhost:5432/diskwave?sslmode=disable"),
		redisAddr:       envOr("REDIS_ADDR", "localhost:6379"),
		storageType:     envOr("STORAGE_TYPE", "local"),
		localStorageDir: envOr("LOCAL_STORAGE_DIR", "/mount/diskwave"),
		minioEndpoint:   envOr("MINIO_ENDPOINT", "localhost:9000"),
		minioAccessKey:  envOr("MINIO_ACCESS_KEY", "diskwave"),
		minioSecretKey:  envOr("MINIO_SECRET_KEY", "diskwave123"),
		minioBucket:     envOr("MINIO_BUCKET", "diskwave"),
		minioUseSSL:     os.Getenv("MINIO_USE_SSL") == "true",
		stagingDir:      envOr("STAGING_DIR", "/tmp/diskwave-staging"),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}