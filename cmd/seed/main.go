package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/seed"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := config.LoadDotEnv(); err != nil && !errors.Is(err, config.ErrNoEnvFileFound) && !errors.Is(err, os.ErrNotExist) {
		slog.Error("load .env", "error", err)
		os.Exit(1)
	}

	var (
		clientIDRaw = flag.String("client-id", "", "provider user id to seed")
		email       = flag.String("email", "demo-provider@example.com", "email for the seeded demo provider")
		password    = flag.String("password", "Password123!", "password for the seeded demo provider")
		fullName    = flag.String("full-name", "Alex Rivera Studio", "display name for the seeded demo provider")
		reset       = flag.Bool("reset", true, "reset existing demo data for this provider before seeding")
	)

	flag.Parse()

	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		slog.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	var clientID uuid.UUID
	var err error
	if strings.TrimSpace(*clientIDRaw) != "" {
		clientID, err = uuid.Parse(strings.TrimSpace(*clientIDRaw))
		if err != nil {
			slog.Error("invalid client-id", "error", err)
			os.Exit(1)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("open database pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		slog.Error("ping database", "error", err)
		os.Exit(1)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		slog.Error("begin transaction", "error", err)
		os.Exit(1)
	}
	defer tx.Rollback(ctx)

	result, err := seed.SeedDemoProvider(ctx, tx, seed.Input{
		ClientID: clientID,
		Email:    *email,
		Password: *password,
		FullName: *fullName,
		Reset:    *reset,
	})
	if err != nil {
		slog.Error("seed demo provider", "error", err)
		os.Exit(1)
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("commit seed transaction", "error", err)
		os.Exit(1)
	}

	fmt.Printf("Seeded demo provider\nclient_id: %s\nemail: %s\npassword: %s\n", result.ClientID, result.Email, result.Password)
	if result.CredentialsPreserved {
		fmt.Println("credentials_preserved: true")
	}
}
