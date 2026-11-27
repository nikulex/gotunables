// Package pg keeps gotunables settings in Postgres, in a schema of its own (tunables), so it never
// touches your application tables, and syncs running instances instantly with LISTEN/NOTIFY.
//
//	db, _ := sql.Open("pgx", dsn) // the pgx stdlib driver: github.com/jackc/pgx/v5/stdlib
//	t, err := tunables.New(Config{}, tunables.WithStore(pg.New(db, "shop")))
//	defer t.Close()
//
// The whole settings struct is one JSONB row, keyed by the name you pass to New (one row per app):
//
//	tunables.settings (name text primary key, value jsonb, updated_at timestamptz)
//
// Every Save also sends a notification on the "tunables" channel (in the same transaction, so it is delivered
// on commit). Instances of the same app listen, and reload the row as soon as another one changes it. The
// listener holds one connection of db for as long as the Tunables is open, and it needs the pgx stdlib driver.
//
// The schema and the table are created on first use (CREATE IF NOT EXISTS), so the role needs CREATE on the
// database; or create them yourself, see Schema.
package pg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
)

// Schema is the SQL that creates the schema and the table. Run it in a migration if the application role
// cannot create schemas; New then finds them in place.
const Schema = `CREATE SCHEMA IF NOT EXISTS tunables;
CREATE TABLE IF NOT EXISTS tunables.settings (
	name       text        PRIMARY KEY,
	value      jsonb       NOT NULL,
	updated_at timestamptz NOT NULL DEFAULT now()
);`

// channel is the LISTEN/NOTIFY channel; the payload is "<app name>|<store id>".
const channel = "tunables"

// initialBackoff is the first wait before reconnecting a dropped listener; it doubles up to 30s.
const initialBackoff = 200 * time.Millisecond

// timeout bounds every query and the start of the listener: tunables.Store has no context.
const timeout = 5 * time.Second

// Store implements tunables.Store and tunables.Watcher on Postgres.
type Store struct {
	db   *sql.DB
	name string
	id   string // tells our own notifications from other instances'

	mu    sync.Mutex
	ready bool
}

// New returns a Store that keeps the settings of the app called name. The name must be stable: it is the
// row key, and instances with the same name sync with each other.
func New(db *sql.DB, name string) *Store { return &Store{db: db, name: name, id: rand.Text()} }

func (s *Store) ensure(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, Schema); err != nil {
		return fmt.Errorf("create schema tunables: %w", err)
	}
	s.ready = true
	return nil
}

// Load decodes the saved row into dst (a *T holding the defaults). Nothing saved yet is not an error.
func (s *Store) Load(dst any) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := s.ensure(ctx); err != nil {
		return err
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM tunables.settings WHERE name = $1`, s.name).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load %q: %w", s.name, err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("parse %q: %w", s.name, err)
	}
	return nil
}

// Save writes src (a *T) as the row of this app and notifies the other instances.
func (s *Store) Save(src any) error {
	b, err := json.Marshal(src)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := s.ensure(ctx); err != nil {
		return err
	}
	// One statement, so the notification is part of the same transaction as the write.
	_, err = s.db.ExecContext(ctx, `
		WITH up AS (
			INSERT INTO tunables.settings (name, value) VALUES ($1, $2::jsonb)
			ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
			RETURNING name)
		SELECT pg_notify('`+channel+`', $3) FROM up`,
		s.name, string(b), s.name+"|"+s.id)
	if err != nil {
		return fmt.Errorf("save %q: %w", s.name, err)
	}
	return nil
}

// Watch listens for changes made by other instances of the same app and calls onChange for each. If the
// connection drops it reconnects (from 200ms, backing off up to 30s) and calls onChange once it is back, since a change
// may have been missed meanwhile. It fails if the listener cannot start, e.g. when db does not use the pgx
// stdlib driver.
func (s *Store) Watch(onChange func()) (stop func(), err error) {
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan error, 1) // the first outcome: nil once LISTEN is on, or why it cannot start
	done := make(chan struct{})

	go func() {
		defer close(done)
		started := false
		backoff := initialBackoff
		for {
			err := s.listenOnce(ctx, func() {
				backoff = initialBackoff
				if !started {
					started = true
					ready <- nil
					return
				}
				onChange()
			}, onChange)
			if ctx.Err() != nil {
				return
			}
			if !started {
				ready <- err
				return
			}
			slog.Warn("pg: listener lost, reconnecting", "error", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}()

	select {
	case err := <-ready:
		if err != nil {
			cancel()
			<-done
			return nil, err
		}
	case <-time.After(timeout):
		cancel()
		<-done
		return nil, errors.New("pg: timed out starting the listener")
	}
	return func() {
		cancel()
		<-done
	}, nil
}

// listenOnce takes a connection, LISTENs on it and relays other instances' notifications until it fails or
// ctx ends. started runs once LISTEN is on.
func (s *Store) listenOnce(ctx context.Context, started, notified func()) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck // after Raw reports a bad conn the pool has already dropped it

	return conn.Raw(func(dc any) error {
		sc, ok := dc.(*stdlib.Conn)
		if !ok {
			return errors.New("pg: instant sync needs the pgx stdlib driver (sql.Open(\"pgx\", dsn))")
		}
		pc := sc.Conn()
		if _, err := pc.Exec(ctx, "LISTEN "+channel); err != nil {
			return fmt.Errorf("listen: %w", err)
		}
		started()
		for {
			n, err := pc.WaitForNotification(ctx)
			if err != nil {
				// ErrBadConn makes database/sql discard the connection instead of pooling one that still LISTENs.
				return errors.Join(fmt.Errorf("wait for notification: %w", err), driver.ErrBadConn)
			}
			if strings.HasPrefix(n.Payload, s.name+"|") && n.Payload != s.name+"|"+s.id {
				notified()
			}
		}
	})
}
