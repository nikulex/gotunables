package tunables

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Store keeps the whole settings struct. Without a Store, changes live in memory and are lost on restart.
//
// Both methods get a pointer to your struct type (*T), so a Store is just "encode it" and "decode it":
//
//	func (s myStore) Load(dst any) error { return json.Unmarshal(s.read(), dst) }
//	func (s myStore) Save(src any) error { return s.write(json.Marshal(src)) }
type Store interface {
	// Load fills dst (a *T). It already holds the defaults, so fields missing from the saved data keep them
	// (decoders like encoding/json and yaml do that on their own). Do nothing when nothing is saved yet.
	Load(dst any) error
	// Save writes src (a *T).
	Save(src any) error
}

// Watcher is an optional extension of Store for instant sync between processes that share a Store.
// If the Store passed to New implements it, New starts watching and every change made elsewhere reaches
// Get() right away; Close stops it.
type Watcher interface {
	// Watch starts listening and returns once it is set up (an error means instant sync is not possible, and
	// New fails). It then calls onChange, from any goroutine, after every change made by another process, and
	// after a lost connection is restored (a change may have been missed). Do not call onChange before Watch
	// returns. stop ends the watching and waits for it.
	Watch(onChange func()) (stop func(), err error)
}

// File returns a Store that keeps the struct in a JSON file, written atomically. Durations are saved by
// encoding/json as nanoseconds; use FileWith and a YAML codec to get "500ms".
func File(path string) Store {
	return FileWith(path, func(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }, json.Unmarshal)
}

// FileWith is File with your own codec, e.g. yaml.Marshal and yaml.Unmarshal.
func FileWith(path string, marshal func(any) ([]byte, error), unmarshal func([]byte, any) error) Store {
	return fileStore{path: path, marshal: marshal, unmarshal: unmarshal}
}

type fileStore struct {
	path      string
	marshal   func(any) ([]byte, error)
	unmarshal func([]byte, any) error
}

func (s fileStore) Load(dst any) error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(b) == 0) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	if err := s.unmarshal(b, dst); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	return nil
}

func (s fileStore) Save(src any) (err error) {
	b, err := s.marshal(src)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if rmErr := os.Remove(tmp.Name()); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				err = errors.Join(err, rmErr)
			}
		}
	}()
	if _, err = tmp.Write(append(b, '\n')); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err = tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
