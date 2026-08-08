package repository

import (
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound          = errors.New("agreement record not found")
	ErrConflict          = errors.New("agreement record changed concurrently")
	ErrLeaseLost         = errors.New("agreement job lease is no longer owned by this worker")
	ErrInvalidTransition = errors.New("agreement state transition is not allowed")
)

type Repository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}
