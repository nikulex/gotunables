// Package tunables is declarative runtime config for Go: describe settings as a struct, read them as plain
// fields, and change them live from a built-in web UI or an HTTP API, without a redeploy.
//
//	type Config struct {
//		Checkout struct {
//			Enabled  bool   `json:"enabled" tune:"name:New checkout; desc:Use the new flow"`
//			MaxItems int    `json:"max_items" tune:"default:10; min:1; max:100"`
//			Color    string `json:"color" tune:"default:blue; enum:blue,green,red"`
//		} `json:"checkout" tune:"name:Checkout"`
//	}
//
//	t, err := tunables.New(Config{}, tunables.WithName("shop"), tunables.WithFile("tunables.json"))
//	...
//	go http.ListenAndServe("127.0.0.1:9090", t.Handler()) // keep it off the public interface
//
//	if t.Get().Checkout.Enabled { ... }
//
// Supported field types: bool, string (optionally an enum), signed and unsigned integers of every size, floats
// (float32, float64) and time.Duration; a slice []T or a Set[T] of any of those (the options below then
// apply to each item); nested and embedded structs become groups. An integer or a string (also a slice or a Set of
// them, or a pointer: one or none) that holds ids of things from elsewhere takes `source:name` and is chosen by
// searching a DataSource, see WithSource.
//
// The key of a field is taken from its json tag, then yaml, then the lower-cased Go name (`json:"-"` skips
// it). Everything else goes into one tag, `tune`, as `key:value` options separated by ";":
//
//	key      the key segment, instead of the json/yaml name
//	name     display name in the UI (also on a nested struct: the group title)
//	desc     description in the UI (also on a nested struct)
//	default  the default (otherwise the value in the struct passed to New)
//	enum     allowed values of a string field: enum:blue,green,red
//	min,max  bounds of a number; durations like min:10ms (for a list or a set: of each item)
//	minlen,maxlen  how many items a list or a set may hold
//	source   the DataSource the ids are picked from (registered with WithSource)
//	custom   with source: an id the source does not know may be added by hand (custom:true)
//
// `tune:"-"` skips a field.
package tunables

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrUnknownKey is returned for a key that is not in the schema.
	ErrUnknownKey = errors.New("tunables: unknown key")
	// ErrInvalidValue is returned for a value the field does not accept.
	ErrInvalidValue = errors.New("tunables: invalid value")
)

// valueError is ErrInvalidValue with the key and the reason, so the API can show just the reason.
type valueError struct{ key, reason string }

func (e *valueError) Error() string {
	return fmt.Sprintf("%v: %s: %s", ErrInvalidValue, e.key, e.reason)
}
func (e *valueError) Unwrap() error { return ErrInvalidValue }

// Tunables holds the live settings of type T.
type Tunables[T any] struct{ c *core }

// New builds the schema of T by reflection and loads the saved struct from the Store (if any).
// initial supplies the defaults; `default` options win over it.
func New[T any](initial T, opts ...Option) (*Tunables[T], error) {
	typ := reflect.TypeFor[T]()
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("tunables: T must be a struct, got %s", typ)
	}
	o := options{log: slog.Default()}
	for _, opt := range opts {
		opt(&o)
	}
	if o.name == "" {
		o.name = filepath.Base(os.Args[0])
	}
	c, err := newCore(typ, reflect.ValueOf(initial), o)
	if err != nil {
		return nil, err
	}
	return &Tunables[T]{c: c}, nil
}

// Get returns the current settings. The result is an immutable snapshot: read it, don't modify it, and call
// Get again to see later changes. It is cheap and safe for concurrent use.
func (t *Tunables[T]) Get() *T {
	return t.c.state.Load().cfg.Interface().(*T)
}

// Set changes one setting by key ("checkout.max_items"); value is a Go value of the field's type (a
// time.Duration for durations). It runs the same validation as the UI.
func (t *Tunables[T]) Set(key string, value any) error {
	raw, err := json.Marshal(wireValue(reflect.ValueOf(value)))
	if err != nil {
		return &valueError{key: key, reason: err.Error()}
	}
	return t.c.set(key, raw)
}

// wireValue turns a Go value into what the API takes: durations become strings ("1m30s"), also inside a list or a
// Set, whose items go in an array.
func wireValue(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	if v.Type() == durationType {
		return time.Duration(v.Int()).String()
	}
	switch v.Kind() {
	case reflect.Pointer: // a setting of one entry or none
		if v.IsNil() {
			return nil
		}
		return wireValue(v.Elem())
	case reflect.Slice:
		if v.IsNil() {
			return []any{}
		}
		fallthrough
	case reflect.Array:
		out := make([]any, v.Len())
		for i := range out {
			out[i] = wireValue(v.Index(i))
		}
		return out
	case reflect.Map:
		if _, ok := v.Interface().(setMarker); ok {
			out := make([]any, 0, v.Len())
			for it := v.MapRange(); it.Next(); {
				out = append(out, wireValue(it.Key()))
			}
			slices.SortFunc(out, cmpAny)
			return out
		}
	}
	return v.Interface()
}

// Reset puts one setting back to its default.
func (t *Tunables[T]) Reset(key string) error { return t.c.reset(key) }

// Close stops watching the Store for changes made by other processes (see Watcher). Get keeps working.
func (t *Tunables[T]) Close() { t.c.close() }

// Option configures New.
type Option func(*options)

type options struct {
	name, description, token string
	store                    Store
	log                      *slog.Logger
	sources                  []*source
	observer                 func(Check)
	err                      error // the first mistake in an option (WithSource): New returns it
}

// WithName sets the source name shown in the UI and in /meta (default: the executable name).
func WithName(name string) Option { return func(o *options) { o.name = name } }

// WithDescription sets the source description shown in the UI and in /meta.
func WithDescription(d string) Option { return func(o *options) { o.description = d } }

// WithStore persists changes in s.
func WithStore(s Store) Option { return func(o *options) { o.store = s } }

// WithFile persists changes in a JSON file.
func WithFile(path string) Option { return WithStore(File(path)) }

// WithToken requires "Authorization: Bearer <token>" on the API. The UI page itself stays public and asks
// for the token.
func WithToken(token string) Option { return func(o *options) { o.token = token } }

// WithObserver is told of every call of IsEnabled of the features of the settings: which feature, which of its groups let the
// request in (if one did), and the answer. It is how a program counts them; the check itself waits for it, so make it quick. A
// Matcher reports too. The metrics/prom module has one that counts for Prometheus.
//
//	tunables.New(cfg, tunables.WithObserver(func(c tunables.Check) { checks.WithLabelValues(c.Feature, c.Group).Inc() }))
func WithObserver(fn func(Check)) Option { return func(o *options) { o.observer = fn } }

// WithLogger sets the logger for warnings (default: slog.Default()).
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.log = l } }

// core is the non-generic part: everything works on reflect values of *T.
type core struct {
	typ      reflect.Type
	fields   []*field
	groups   []*group
	byKey    map[string]*field
	defaults reflect.Value // *T holding the defaults
	opts     options
	sources  map[string]*source

	features   []*featureRef        // the Features of the settings
	groupSets  []*groupSet          // the Groups of each
	groupByKey map[string]*groupSet // by the key of the list: "features.new_checkout.groups"

	mu    sync.Mutex // serializes writes and reloads
	state atomic.Pointer[snapshot]

	stop      func() // ends the Store's Watch, if any
	closeOnce sync.Once
}

// snapshot is immutable once published.
type snapshot struct {
	cfg reflect.Value // *T
}

func newCore(typ reflect.Type, initial reflect.Value, o options) (*core, error) {
	cfg := reflect.New(typ)
	cfg.Elem().Set(initial)

	if o.err != nil {
		return nil, o.err
	}
	w, err := walkAll(typ, cfg.Elem())
	if err != nil {
		return nil, err
	}
	fields, groups := w.fields, w.groups
	c := &core{typ: typ, fields: fields, groups: groups, byKey: make(map[string]*field, len(fields)), opts: o, sources: make(map[string]*source, len(o.sources)),
		features: w.features, groupSets: w.groupSets, groupByKey: make(map[string]*groupSet, len(w.groupSets))}
	for _, gs := range w.groupSets {
		c.groupByKey[gs.key] = gs
	}
	for _, s := range o.sources {
		if c.sources[s.name] != nil {
			return nil, fmt.Errorf("tunables: source %q is registered twice", s.name)
		}
		c.sources[s.name] = s
	}
	leaves := slices.Clone(fields) // the fields of the settings, and the ones of the targeting of each group
	for _, gs := range c.groupSets {
		leaves = append(leaves, gs.fields...)
	}
	for _, f := range leaves { // the sources that imported packages register (RegisterSource), unless the program registered its own under the name
		if src, ok := registeredSource(f.source); ok && c.sources[f.source] == nil {
			c.sources[f.source] = src
		}
	}
	for _, f := range leaves {
		if err := c.checkSource(f); err != nil {
			return nil, fmt.Errorf("tunables: field %s: %w", f.key, err)
		}
	}
	for _, f := range fields {
		c.byKey[f.key] = f
	}
	for _, gs := range c.groupSets { // the groups the program gave are checked, and get the ids they lack
		def := gs.clone(gs.def)
		for i := 0; i < def.Len(); i++ { // the same ids every time the program starts
			if id := def.Index(i).Field(groupID); id.String() == "" {
				id.SetString(fmt.Sprintf("default-%d", i+1))
			}
		}
		if err := gs.check(c, def, true); err != nil {
			return nil, fmt.Errorf("tunables: %w", err)
		}
		gs.def = def
		cfg.Elem().FieldByIndex(gs.index).Set(def)
	}
	c.defaults = reflect.New(typ)
	c.defaults.Elem().Set(cfg.Elem())

	if err := c.start(cfg); err != nil {
		c.close() // outside the lock: stopping the watcher waits for its goroutine
		return nil, err
	}
	return c, nil
}

// start publishes the first snapshot. The watcher is started before the first load, so a change that lands
// in between is not missed; its callbacks wait on c.mu until the snapshot exists.
func (c *core) start(defaults reflect.Value) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if w, ok := c.opts.store.(Watcher); ok {
		stop, err := w.Watch(c.reload)
		if err != nil {
			return fmt.Errorf("tunables: watch: %w", err)
		}
		c.stop = stop
	}
	cfg := defaults
	if c.opts.store != nil {
		var err error
		if cfg, err = c.loadStored(); err != nil {
			return err
		}
	}
	c.publish(cfg)
	return nil
}

// publish makes a config the current one.
func (c *core) publish(cfg reflect.Value) {
	c.bind(cfg)
	c.state.Store(&snapshot{cfg: cfg})
}

// loadStored builds a config from the defaults plus whatever the Store holds. Invalid saved values fall back
// to the default.
func (c *core) loadStored() (reflect.Value, error) {
	// Decode onto a copy that already holds the defaults, so fields missing from the saved data keep them.
	loaded := reflect.New(c.typ)
	loaded.Elem().Set(c.defaults.Elem())
	for _, gs := range c.groupSets { // the stored groups are read onto nothing: a list that is not there keeps the default, one that is replaces it
		loaded.Elem().FieldByIndex(gs.index).SetZero()
	}
	if err := c.opts.store.Load(loaded.Interface()); err != nil {
		return reflect.Value{}, fmt.Errorf("tunables: load: %w", err)
	}
	cfg := reflect.New(c.typ)
	cfg.Elem().Set(c.defaults.Elem())
	for _, f := range c.fields {
		v := f.read(loaded.Elem().FieldByIndex(f.index))
		if err := f.check(v); err != nil {
			c.opts.log.Warn("tunables: saved value is invalid, using the default", "key", f.key, "error", err)
			continue
		}
		f.write(cfg.Elem().FieldByIndex(f.index), v)
	}
	for _, gs := range c.groupSets {
		stored := loaded.Elem().FieldByIndex(gs.index)
		if stored.IsNil() {
			continue // none saved: the default (it is in cfg already)
		}
		groups := gs.clone(stored)
		if err := gs.check(c, groups, false); err != nil {
			return reflect.Value{}, err
		}
		cfg.Elem().FieldByIndex(gs.index).Set(groups)
	}
	return cfg, nil
}

// reload re-reads the Store after a change made elsewhere. It holds the lock for the whole read-and-swap, so
// it cannot overwrite a newer local change with older data.
func (c *core) reload() {
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg, err := c.loadStored()
	if err != nil {
		c.opts.log.Warn("tunables: reload failed, keeping the current settings", "error", err)
		return
	}
	c.publish(cfg)
}

func (c *core) close() {
	c.closeOnce.Do(func() {
		if c.stop != nil {
			c.stop()
		}
	})
}

// checkSource makes sure the data source of a field is registered and has the type of its ids.
func (c *core) checkSource(f *field) error {
	if f.source == "" {
		return nil
	}
	src, ok := c.sources[f.source]
	if !ok {
		return fmt.Errorf("source %q is not registered (see WithSource)", f.source)
	}
	item := f
	if f.collection() {
		item = f.elem
	}
	if item.typ != src.idType {
		return fmt.Errorf("source %q has ids of type %s, the field holds %s", f.source, src.idType, item.typ)
	}
	return nil
}

func (c *core) lookup(key string) (*field, error) {
	f, ok := c.byKey[key]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
	return f, nil
}

func (c *core) set(key string, raw json.RawMessage) error {
	if gs, ok := c.groupByKey[key]; ok {
		groups, err := gs.parse(c, raw)
		if err != nil {
			return err
		}
		return c.commit(func(cfg reflect.Value) { cfg.Elem().FieldByIndex(gs.index).Set(groups) })
	}
	f, err := c.lookup(key)
	if err != nil {
		return err
	}
	v, err := f.parse(raw)
	if err != nil {
		return &valueError{key: key, reason: err.Error()}
	}
	if err := c.verify(f, v); err != nil {
		return err
	}
	return c.commit(func(cfg reflect.Value) { f.write(cfg.Elem().FieldByIndex(f.index), v) })
}

func (c *core) reset(key string) error {
	if gs, ok := c.groupByKey[key]; ok {
		return c.commit(func(cfg reflect.Value) { cfg.Elem().FieldByIndex(gs.index).Set(gs.clone(gs.def)) })
	}
	f, err := c.lookup(key)
	if err != nil {
		return err
	}
	return c.commit(func(cfg reflect.Value) { f.write(cfg.Elem().FieldByIndex(f.index), f.def) })
}

// commit applies mutate to a copy, saves it, and only then publishes it, so a failed save changes nothing.
func (c *core) commit(mutate func(cfg reflect.Value)) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	next := reflect.New(c.typ)
	next.Elem().Set(c.state.Load().cfg.Elem())
	mutate(next)
	if c.opts.store != nil {
		if err := c.opts.store.Save(next.Interface()); err != nil {
			return fmt.Errorf("tunables: save: %w", err)
		}
	}
	c.publish(next)
	return nil
}
