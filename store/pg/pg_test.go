package pg

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
)

// Runs against a real Postgres: TUNABLES_PG_DSN=postgres://user@127.0.0.1:5432/db go test ./...
// Skipped without it. It creates the tunables schema in that database and removes only its own rows.

type cfg struct {
	A      int `json:"a"`
	Nested struct {
		B string `json:"b"`
	} `json:"nested"`
}

func defaults() cfg {
	c := cfg{A: 5}
	c.Nested.B = "x"
	return c
}

func open(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TUNABLES_PG_DSN")
	if dsn == "" {
		t.Skip("TUNABLES_PG_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func uniqueName(t *testing.T, db *sql.DB) string {
	t.Helper()
	name := fmt.Sprintf("test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM tunables.settings WHERE name = $1`, name); err != nil {
			t.Error(err)
		}
	})
	return name
}

func TestEmptyLoadKeepsDefaults(t *testing.T) {
	db := open(t)
	s := New(db, uniqueName(t, db))
	dst := defaults()
	if err := s.Load(&dst); err != nil {
		t.Fatal(err)
	}
	if dst != defaults() {
		t.Errorf("nothing saved: the defaults must stay, got %+v", dst)
	}
}

func TestSaveLoadRoundTripAndUpsert(t *testing.T) {
	db := open(t)
	name := uniqueName(t, db)
	s := New(db, name)

	c := defaults()
	c.A = 42
	c.Nested.B = "hello"
	if err := s.Save(&c); err != nil {
		t.Fatal(err)
	}
	c.A = 43 // upsert: the same row is replaced
	if err := s.Save(&c); err != nil {
		t.Fatal(err)
	}

	got := defaults()
	if err := New(db, name).Load(&got); err != nil { // a fresh Store, as after a restart
		t.Fatal(err)
	}
	if got.A != 43 || got.Nested.B != "hello" {
		t.Errorf("round trip: %+v", got)
	}

	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM tunables.settings WHERE name = $1`, name).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("want one row per app, got %d", rows)
	}
}

func TestMissingFieldsKeepDefaults(t *testing.T) {
	db := open(t)
	name := uniqueName(t, db)
	s := New(db, name)
	if err := s.Load(new(cfg)); err != nil { // makes sure the table exists
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tunables.settings (name, value) VALUES ($1, '{"a": 9}')`, name); err != nil {
		t.Fatal(err)
	}
	dst := defaults()
	if err := s.Load(&dst); err != nil {
		t.Fatal(err)
	}
	if dst.A != 9 || dst.Nested.B != "x" {
		t.Errorf("a field the row lacks must keep its default: %+v", dst)
	}
}

func TestAppsAreIsolated(t *testing.T) {
	db := open(t)
	a, b := New(db, uniqueName(t, db)), New(db, uniqueName(t, db))
	c := defaults()
	c.A = 1
	if err := a.Save(&c); err != nil {
		t.Fatal(err)
	}
	got := defaults()
	if err := b.Load(&got); err != nil {
		t.Fatal(err)
	}
	if got.A != 5 {
		t.Errorf("another app's settings leaked: %+v", got)
	}
}

func TestSchemaIsItsOwn(t *testing.T) {
	db := open(t)
	if err := New(db, uniqueName(t, db)).Load(new(cfg)); err != nil {
		t.Fatal(err)
	}
	var n int
	err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'tunables' AND table_name = 'settings'`).Scan(&n)
	if err != nil || n != 1 {
		t.Errorf("tunables.settings must exist: n=%d err=%v", n, err)
	}
}

func TestWatchNotifiesOtherInstancesOnly(t *testing.T) {
	db := open(t)
	name := uniqueName(t, db)
	a, b := New(db, name), New(db, name)
	other := New(db, uniqueName(t, db)) // another app

	got := make(chan struct{}, 8)
	stop, err := b.Watch(func() { got <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	c := defaults()
	quiet := func(what string) {
		t.Helper()
		select {
		case <-got:
			t.Errorf("unexpected notification: %s", what)
		case <-time.After(300 * time.Millisecond):
		}
	}

	if err := b.Save(&c); err != nil {
		t.Fatal(err)
	}
	quiet("an instance must not be notified about its own change")

	if err := other.Save(&c); err != nil {
		t.Fatal(err)
	}
	quiet("a change of another app")

	start := time.Now()
	if err := a.Save(&c); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
		t.Logf("another instance's change arrived in %v", time.Since(start))
	case <-time.After(2 * time.Second):
		t.Fatal("no notification for another instance's change")
	}
}

func TestWatchStopReleasesTheConnection(t *testing.T) {
	db := open(t)
	s := New(db, uniqueName(t, db))
	stop, err := s.Watch(func() {})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	// The listening connection must have been dropped, not returned to the pool still LISTENing.
	if open := db.Stats().OpenConnections; open != db.Stats().Idle+db.Stats().InUse {
		t.Errorf("inconsistent pool: %+v", db.Stats())
	}
	if db.Stats().InUse != 0 {
		t.Errorf("the listener still holds a connection: %+v", db.Stats())
	}
}
