package chunkie

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

func envInt(k string, d int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return d
}

func envStr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// databaseURL builds the journal DSN. DATABASE_URL wins (local dev); in
// the cluster the password arrives as a mounted Secret file so rotation
// rides the kubelet sync, keeping this Deployment reloader-free (the
// behavior hot-reload doctrine).
func databaseURL() (string, error) {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn, nil
	}
	pwFile := os.Getenv("PG_PASSWORD_FILE")
	if pwFile == "" {
		return "", fmt.Errorf("neither DATABASE_URL nor PG_PASSWORD_FILE set")
	}
	pw, err := os.ReadFile(pwFile)
	if err != nil {
		return "", err
	}
	host, db, user := os.Getenv("PG_HOST"), os.Getenv("PG_DATABASE"), os.Getenv("PG_USER")
	if host == "" || db == "" || user == "" {
		return "", fmt.Errorf("PG_PASSWORD_FILE is set but PG_HOST/PG_DATABASE/PG_USER are not")
	}
	return fmt.Sprintf("postgresql://%s:%s@%s/%s?sslmode=require&pool_max_conns=4",
		user, url.QueryEscape(strings.TrimSpace(string(pw))), host, db), nil
}

func devTickRateHandler(registry *chunks, allowedParks map[string]bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		chunk := r.URL.Query().Get("chunk")
		if !allowedParks[chunk] {
			http.NotFound(w, r)
			return
		}
		hz, err := strconv.Atoi(r.URL.Query().Get("hz"))
		if err != nil || hz < minTickHz || hz > maxTickHz {
			http.Error(w, fmt.Sprintf("hz must be an integer in %d..%d", minTickHz, maxTickHz), http.StatusBadRequest)
			return
		}
		a, ok := registry.current(chunk)
		if !ok {
			http.Error(w, "chunk has no live authority; connect the drill client first", http.StatusConflict)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := a.requestRate(ctx, hz); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]any{"chunk": chunk, "rateHz": hz})
	}
}
