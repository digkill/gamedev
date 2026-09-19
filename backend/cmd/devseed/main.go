// devseed issues a local-only test token. It is not a production identity flow.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/digkill/gamedev/backend/internal/platform/config"
	"github.com/digkill/gamedev/backend/internal/projects"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if cfg.Env != "development" {
		log.Fatal("devseed runs only with APP_ENV=development")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal("cannot connect to local database")
	}
	defer db.Close()
	id, err := projects.NewUUID()
	if err != nil {
		log.Fatal(err)
	}
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		log.Fatal(err)
	}
	token := "sbx_dev_" + base64.RawURLEncoding.EncodeToString(bytes[:])
	hash := sha256.Sum256([]byte(token))
	tx, err := db.Begin(ctx)
	if err != nil {
		log.Fatal("cannot begin transaction")
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "INSERT INTO users(id, display_name) VALUES ($1, $2)", id, "Local developer"); err != nil {
		log.Fatal("cannot create local user")
	}
	if _, err := tx.Exec(ctx, "INSERT INTO access_tokens(token_hash, user_id, expires_at) VALUES ($1, $2, now() + interval '1 day')", hash[:], id); err != nil {
		log.Fatal("cannot create local token")
	}
	if err := tx.Commit(ctx); err != nil {
		log.Fatal("cannot commit local token")
	}
	fmt.Fprintln(os.Stdout, token)
}
