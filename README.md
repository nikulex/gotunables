# gotunables

Declarative runtime config for Go. Describe settings as structs and expose them as a tuneboard-compatible source (name, description, schema, live values).

> Early version, the API may change.

```go
type Config struct {
	Checkout struct {
		Enabled  bool   `json:"enabled" tune:"name:New checkout; desc:Use the new flow"`
		MaxItems int    `json:"max_items" tune:"default:10; min:1; max:100"`
		Color    string `json:"color" tune:"default:blue; enum:blue,green,red"`
	} `json:"checkout" tune:"name:Checkout"`
	Retry struct {
		Backoff time.Duration `yaml:"backoff" tune:"default:500ms; min:10ms; max:1m"`
	} `yaml:"retry"`
}

t, err := tunables.New(Config{},
	tunables.WithName("shop"),
	tunables.WithFile("tunables.json"), // optional: keep changes between restarts
)
if err != nil { ... }

// the settings UI + API; keep it off public listeners (VPN / loopback behind a proxy)
go http.ListenAndServe("127.0.0.1:9090", t.Handler())

if t.Get().Checkout.Enabled { ... } // a plain struct, a change shows up on the next Get()
```

## Try it

```
go run ./demo    # open http://127.0.0.1:6767/, change a value, the terminal prints the new config
```

The library opens no port itself: you give `t.Handler()` to your own server, on any address. `6767` is just the
convention used by the demo (and what tuneboard will suggest for a new source).

## Schema

- Field types: `bool`, `string`, every size of int and uint (`int8` ... `uint64`), `float32`, `float64`, `time.Duration`.
  A slice `[]T` or a `tunables.Set[T]` of any of those is a list or a set. Nested and embedded structs are groups.
  A pointer `*T` is a setting only with `source:` (see below). Anything else (maps, other pointers, `[]byte`, lists of
  lists) is an error, unless the field is tagged `tune:"-"`.
- Key = path of names joined by dots (`checkout.max_items`). A field's name comes from its `json` tag, then
  `yaml`, then the lower-cased Go name (`json:"-"` skips it).
- Everything else is one tag, `tune`, with `key:value` options separated by `;`:

  | option | |
  |---|---|
  | `name` | display name in the UI (on a nested struct: the group title). Default: the key segment with a capital letter, `max_items` becomes "Max items" |
  | `desc` | description (also on a nested struct) |
  | `default` | the default (otherwise the value in the struct you pass to `New`) |
  | `enum` | allowed values of a string field: `enum:blue,green,red` |
  | `min`, `max` | bounds of a number; durations like `min:10ms`. For a list or a set: of each item |
  | `minlen`, `maxlen` | how many items a list or a set may hold (at most 1000 anyway) |
  | `source` | the data source the ids are picked from (see "Picking from existing data") |
  | `key` | the key segment instead of the json/yaml name |
  | `precision` | digits shown after the point of a float (0 to 10): `precision:2` draws `0.20` and `1.00`, so the number never changes width. Default: as many as the step has |
  | `ui` | the component that edits the field; the first one is the default: bool `switch` or `checkbox`, enum `select` or `buttons`, number `counter`, `slider` (needs `min` and `max`) or `input`, string `input` or `textarea`, duration `input` |

  `tune:"-"` skips a field. A typo in an option name is an error at `New`.
- **Lists and sets.** `[]string` keeps the order and the repeats; `tunables.Set[int64]` has neither (`Has`, `Len`,
  `Items`, `NewSet`), and is stored as a sorted JSON array. In the UI both are chips with an input of the item's
  own type; `enum`, `min`, `max` and `precision` apply to each item, and a `default` is `1,2,3` or a JSON array
  (`["a,b","c"]` when an item has a comma). A saved list with one invalid item falls back to the default as a
  whole. `Set("admins", []int64{1, 2})` takes a slice or a Set.

  ```go
  Admins  tunables.Set[int64] `json:"admins" tune:"desc:Telegram ids; min:1"`
  Origins []string            `json:"origins" tune:"maxlen:5; default:https://a.example"`
  Waits   []time.Duration     `json:"waits" tune:"default:1s,5s; min:100ms; max:10m"`
  ```
- **Picking from existing data.** A setting that holds ids of things that live elsewhere (users, communities,
  products) is not typed in: it is searched for by name, with a picture. Register a `DataSource` and refer to it:

  ```go
  t, err := tunables.New(Config{}, tunables.WithSource("users", tunables.DataSource[uint]{
      // what is typed, at most limit entries; an empty query is the list that opens first
      Search: func(ctx context.Context, query string, limit int) ([]tunables.Item[uint], error) { ... },
      // the entries of these ids (the ones that do not exist are left out): shows the chosen, checks a change
      Get: func(ctx context.Context, ids []uint) ([]tunables.Item[uint], error) { ... },
  }))

  type Config struct {
      Admins tunables.Set[uint] `json:"admins" tune:"source:users; maxlen:5"` // any number
      Owner  *uint              `json:"owner" tune:"source:users"`            // one, or nobody (nil)
      Chain  []uint             `json:"chain" tune:"source:users"`            // in this order
  }
  ```

  `tunables.Item[ID]` is `{ID, Name, ImageSrc}`; the picture is small (drawn 24px), an `https` URL or a `data:image/`
  URI. The id type (int, uint, string, of any size) must be the type of the setting or of its item: `New` checks it,
  and that every `source:` is registered. In the UI the chosen ones are chips with their pictures, and a list opens
  under an input as you type (a pause of 250 ms, the earlier request is cancelled). A change is refused when one of its
  ids is not found by `Get`; an id that was saved and deleted later stays, and is shown as "not found". `Search` and
  `Get` are cut off after 5 seconds, `limit` is at most 50, a query at most 100 characters, and what they fail with
  goes to the log, not to the page. `min`, `max`, `enum` and `precision` do not go with `source`. A `Set(key, nil)`
  or `Reset` makes a `*T` nobody. The demo picks users of a public test API (`go run ./demo`, page People).
- `Get()` returns an immutable snapshot. `Set(key, value)` / `Reset(key)` do what the UI does.

## Storage

Without a store, changes live in memory. A store keeps **the struct itself**:

```go
type Store interface {
	Load(dst any) error // dst is a *T already holding the defaults: decode onto it
	Save(src any) error // src is a *T
}
```

- `tunables.WithFile("tunables.json")`: JSON, written atomically (durations are nanoseconds there; use
  `tunables.FileWith(path, yaml.Marshal, yaml.Unmarshal)` for `500ms`).
- Fields missing from the saved data keep their defaults, so new fields are safe to add. A saved value that
  no longer passes `enum`/`min`/`max` falls back to the default (with a warning).
- **`store/`**: more stores, each a **separate module** (its driver is its own dependency; the main module has none).
  Add the one you use:

  | store | instant sync | |
  |---|---|---|
  | `tunables.WithFile` (main module) | no | one JSON file, written atomically |
  | [`store/pg`](store/pg) | yes, `LISTEN/NOTIFY` | Postgres, own schema `tunables` |

  A store is `Load`/`Save` on the struct; if it also implements `Watcher` (`Watch(onChange) (stop, err)`) the
  instances follow each other at once, and if its `Watch` cannot start, `New` fails rather than running unsynced.

  ```go
  // go get github.com/nikulex/gotunables/store/pg
  db, _ := sql.Open("pgx", dsn) // pgx stdlib driver
  t, err := tunables.New(Config{}, tunables.WithStore(pg.New(db, "shop")))
  defer t.Close()
  ```

  `store/pg` keeps one JSONB row per app (`tunables.settings`), creates the schema on first use (the role needs
  `CREATE`, or run `pg.Schema` yourself), sends `NOTIFY` in the same transaction as the write and reconnects
  its listener on its own. Instances of one app must share the name given to `pg.New`.

## HTTP API (protocol 1)

| | |
|---|---|
| `GET /` | the UI: one embedded HTML file, no build, works on phones. Click a group's heading to open it on its own page (`#/checkout.button`) with the breadcrumbs in the navbar (every parent is a link). Edits are collected (the changed fields get an orange *); a sticky header shows **Save** (Ctrl/Cmd+S) and **Discard** while there are any |
| `GET /meta` | `{"name", "description", "protocol": 1}` |
| `GET /schema` | `{"groups": [{key, name, desc}], "fields": [{key, name, type, ui, desc, default, value, modified, enum, min, max, precision}]}`; a field with `source` has `ui: "picker"` and the name of the source, one that may be nobody has `optional: true` and a `null` value; a list or a set has `type: "list"\|"set"` and `item` (the type of an item; `enum`, `min`, `max`, `precision` are the item's), `minlen`, `maxlen`, and its `value` is an array | |
| `PUT /values/{key}` | body `{"value": ...}` (`null` for a `*T` with nobody), returns the updated field |
| `GET /sources/{name}/search?q=&limit=` | the entries of a data source that match: `{"items": [{id, name, image}]}`; `limit` 1 to 50, default 20 |
| `GET /sources/{name}/items?id=&id=` | the entries of these ids (up to 100), the ones that exist: `{"items": [...]}` |
| `DELETE /values/{key}` | back to the default |

Types on the wire: `bool`, `string`, `enum`, `int`, `float`, `duration` (a string like `"500ms"`).
`modified` means "differs from the default".

## Safety

- Anyone who reaches the handler can change your settings. Serve it on a loopback / VPN listener.
- `WithToken` adds a bearer token. Writes also need `Content-Type: application/json` and a same-host `Origin`
  (the reverse proxy must pass the original `Host`).
- The page may show pictures from `https:` and `data:` only (`img-src`), and every picture is asked for by the browser of
  whoever opens the page, with no `Referer`: the picture hosts see their address. Serve pictures from your own host if that matters.
- The sources are read-only and behind the same token as everything else.
- Mount under a prefix with a trailing slash: `mux.Handle("/tunables/", http.StripPrefix("/tunables", t.Handler()))`.
