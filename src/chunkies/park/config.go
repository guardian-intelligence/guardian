package park

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
	host := envStr("PG_HOST", "postgres-products-rw.tenant-guardian-prod.svc:5432")
	db := envStr("PG_DATABASE", "mythra")
	user := envStr("PG_USER", "mythra")
	return fmt.Sprintf("postgresql://%s:%s@%s/%s?sslmode=require&pool_max_conns=4",
		user, url.QueryEscape(strings.TrimSpace(string(pw))), host, db), nil
}

func devTickRateHandler(registry *parks, allowedParks map[string]bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		park := r.URL.Query().Get("park")
		if !allowedParks[park] {
			http.NotFound(w, r)
			return
		}
		hz, err := strconv.Atoi(r.URL.Query().Get("hz"))
		if err != nil || hz < minTickHz || hz > maxTickHz {
			http.Error(w, fmt.Sprintf("hz must be an integer in %d..%d", minTickHz, maxTickHz), http.StatusBadRequest)
			return
		}
		a, ok := registry.current(park)
		if !ok {
			http.Error(w, "park has no live authority; connect the drill client first", http.StatusConflict)
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
		json.NewEncoder(w).Encode(map[string]any{"park": park, "rateHz": hz})
	}
}
