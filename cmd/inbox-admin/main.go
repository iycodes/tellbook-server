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

	"booking/go-server/internal/appdata"
	"booking/go-server/internal/config"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := config.LoadDotEnv(); err != nil && !errors.Is(err, config.ErrNoEnvFileFound) &&
		!errors.Is(err, os.ErrNotExist) {
		slog.Error("load environment", "error", err)
		os.Exit(1)
	}
	conversationRaw := flag.String("conversation-id", "", "conversation UUID")
	action := flag.String("action", "", "disable or enable")
	reason := flag.String("reason", "", "operator reason recorded in the audit log")
	operator := flag.String("operator", "", "operator identity recorded in the audit log")
	flag.Parse()

	conversationID, err := uuid.Parse(strings.TrimSpace(*conversationRaw))
	if err != nil {
		exitError("conversation-id must be a UUID", err)
	}
	disabled := strings.EqualFold(strings.TrimSpace(*action), "disable")
	if !disabled && !strings.EqualFold(strings.TrimSpace(*action), "enable") {
		exitError("action must be disable or enable", nil)
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		exitError("DATABASE_URL is required", nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		exitError("open database", err)
	}
	defer pool.Close()
	result, err := appdata.NewRepository(pool).SetInboxConversationDisabled(
		ctx, conversationID, disabled, *reason, *operator,
	)
	if err != nil {
		exitError("update inbox moderation", err)
	}
	fmt.Printf(
		"conversation=%s disabled=%t changed=%t audit_id=%s\n",
		result.ConversationID, result.Disabled, result.Changed, result.AuditID,
	)
}

func exitError(message string, err error) {
	if err != nil {
		slog.Error(message, "error", err)
	} else {
		slog.Error(message)
	}
	os.Exit(1)
}
