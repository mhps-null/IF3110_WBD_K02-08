package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/config"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/password"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid application configuration", "reason", err.Error())
		os.Exit(1)
	}
	username := strings.TrimSpace(os.Getenv("SEED_ADMIN_USERNAME"))
	plainPassword := os.Getenv("SEED_ADMIN_PASSWORD")
	if username == "" || len(plainPassword) < 14 || plainPassword == "change-this-password" {
		logger.Error("SEED_ADMIN_USERNAME and a non-placeholder SEED_ADMIN_PASSWORD of at least 14 characters are required")
		os.Exit(1)
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		logger.Error("cannot initialize metadata database")
		os.Exit(1)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		logger.Error("cannot connect to metadata database; check database availability and DATABASE_URL")
		os.Exit(1)
	}

	hash, err := password.Hash(plainPassword)
	if err != nil {
		logger.Error("cannot hash seed password")
		os.Exit(1)
	}
	result, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, is_platform_admin, is_active, created_at)
		VALUES ($1, $2, TRUE, TRUE, NOW()) ON CONFLICT (username) DO NOTHING`, username, hash)
	if err != nil {
		logger.Error("cannot seed Platform Admin; verify that metadata schema is installed")
		os.Exit(1)
	}
	count, err := result.RowsAffected()
	if err != nil {
		logger.Error("cannot determine seed result")
		os.Exit(1)
	}
	if count == 0 {
		logger.Info("user already exists; seed did not alter its password or privileges")
		return
	}
	logger.Info("initial Platform Admin created; remove seed credentials from the environment")
}
