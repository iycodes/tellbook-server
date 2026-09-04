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
	"booking/go-server/internal/regionimport"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := config.LoadDotEnv(); err != nil && !errors.Is(err, config.ErrNoEnvFileFound) && !errors.Is(err, os.ErrNotExist) {
		slog.Error("load .env", "error", err)
		os.Exit(1)
	}

	adm1Path := flag.String("adm1", "assets/geography/nigeria/geoBoundaries-NGA-ADM1.geojson", "path to Nigeria ADM1 GeoJSON")
	adm2Path := flag.String("adm2", "assets/geography/nigeria/geoBoundaries-NGA-ADM2.geojson", "path to Nigeria ADM2 GeoJSON")
	flag.Parse()

	adm1, err := os.Open(*adm1Path)
	if err != nil {
		slog.Error("open ADM1 GeoJSON", "error", err)
		os.Exit(1)
	}
	defer adm1.Close()

	adm2, err := os.Open(*adm2Path)
	if err != nil {
		slog.Error("open ADM2 GeoJSON", "error", err)
		os.Exit(1)
	}
	defer adm2.Close()

	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		slog.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("open database pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	result, err := regionimport.New(pool).Import(ctx, adm1, adm2)
	if err != nil {
		slog.Error("import Nigeria regions", "error", err)
		os.Exit(1)
	}

	fmt.Printf(
		"Imported Nigeria regions\nstates: %d\nlgas: %d\nbackfilled_business_locations: %d\nbackfilled_resolved_locations: %d\n",
		result.States,
		result.LGAs,
		result.BackfilledBusinesses,
		result.BackfilledResolved,
	)
}
