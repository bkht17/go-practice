package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// --------- DB ---------

var db *sql.DB

func InitDB(dsn string) {
	var err error
	db, err = sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("ping db: %v", err)
	}
}

// --------- Models ---------

type Job struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Company   string    `json:"company"`
	Salary    int       `json:"salary"`
	CreatedAt time.Time `json:"created_at"`
}

type JobsResponse struct {
	Items       []Job `json:"items"`
	NextAfterID *int  `json:"next_after_id,omitempty"`
}

// --------- Handler ---------

func GetJobsHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	q := r.URL.Query()
	company := q.Get("company")
	limit := parseLimit(q.Get("limit"), 50, 200)

	var afterID *int
	if s := q.Get("after_id"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v <= 0 {
			http.Error(w, "invalid after_id", http.StatusBadRequest)
			return
		}
		afterID = &v
	}

	args := []any{}
	where := []string{}

	if company != "" {
		args = append(args, company)
		where = append(where, fmt.Sprintf("company = $%d", len(args)))
	}

	if afterID != nil {
		var cursorCreatedAt time.Time
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := db.QueryRowContext(ctx,
			"SELECT created_at FROM jobs WHERE id = $1", *afterID,
		).Scan(&cursorCreatedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "after_id not found", http.StatusBadRequest)
				return
			}
			log.Printf("cursor lookup error after_id=%d: %v", *afterID, err)
			http.Error(w, "failed to resolve after_id", http.StatusInternalServerError)
			return
		}

		args = append(args, cursorCreatedAt, cursorCreatedAt, *afterID)
		where = append(where,
			fmt.Sprintf("(created_at > $%d OR (created_at = $%d AND id > $%d))",
				len(args)-2, len(args)-1, len(args)))
	}

	query := "SELECT id, title, company, salary, created_at FROM jobs"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at DESC, id DESC"
	args = append(args, limit)
	query += fmt.Sprintf(" LIMIT $%d", len(args))

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	qStart := time.Now()
	rows, err := db.QueryContext(ctx, query, args...)
	qElapsed := time.Since(qStart)
	w.Header().Set("X-Query-Time", qElapsed.String())
	log.Printf("GET /jobs took=%s sql=%q args=%v", qElapsed, query, args)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	jobs := make([]Job, 0, limit)
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Title, &j.Company, &j.Salary, &j.CreatedAt); err != nil {
			http.Error(w, "scan failed", http.StatusInternalServerError)
			return
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "rows error", http.StatusInternalServerError)
		return
	}

	var nextAfterID *int
	if len(jobs) > 0 {
		last := jobs[len(jobs)-1].ID
		nextAfterID = &last
	}

	resp := JobsResponse{Items: jobs, NextAfterID: nextAfterID}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "    ")
	if err := enc.Encode(resp); err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}

	_ = start
}

// --------- Helpers ---------

func parseLimit(s string, def, max int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// --------- main ---------

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:123@localhost:5432/practice5?sslmode=disable"
	}
	InitDB(dsn)

	http.HandleFunc("/jobs", GetJobsHandler)

	log.Println("Server listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
