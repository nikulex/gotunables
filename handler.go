package tunables

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nikulex/gotunables/ui"
)

// protocol is the version of the source API (/meta, /schema, /values) that tuneboard speaks.
const protocol = 1

// Handler serves the source API and the built-in UI:
//
//	GET    /              the UI
//	GET    /meta          {name, description, protocol}
//	GET    /schema        {fields: [{key, type, desc, default, value, modified, enum, min, max}]}
//	PUT    /values/{key}  {"value": ...}  -> the updated field
//	DELETE /values/{key}  back to the default -> the updated field
//	GET    /sources/{name}/search?q=&limit=&offset=  the entries of a data source that match -> {items: [{id, name, image}], more}
//	GET    /sources/{name}/items?id=&id=     the entries of these ids -> {items: [...]} (the ones that exist)
//
// Mount it under a prefix with http.StripPrefix and a trailing slash:
//
//	mux.Handle("/tunables/", http.StripPrefix("/tunables", t.Handler()))
//
// Don't put it on a public listener: anyone who reaches it can change your settings (WithToken adds a bearer
// token, nothing more). Writes also require a JSON content type and a same-host Origin, and the reverse proxy
// must pass the original Host header.
func (t *Tunables[T]) Handler() http.Handler { return t.c.handler() }

type fieldDTO struct {
	Key       string   `json:"key"`
	Name      string   `json:"name,omitempty"`
	Type      string   `json:"type"`
	Item      string   `json:"item,omitempty"` // a list or a set: the type of an item; enum, min, max and precision are the item's
	UI        string   `json:"ui"`
	Precision *int     `json:"precision,omitempty"` // digits after the point to show for a float
	Desc      string   `json:"desc,omitempty"`
	Default   any      `json:"default"`
	Value     any      `json:"value"`
	Modified  bool     `json:"modified"`
	Enum      []string `json:"enum,omitempty"`
	Min       any      `json:"min,omitempty"`
	Max       any      `json:"max,omitempty"`
	MinLen    *int     `json:"minlen,omitempty"` // a list or a set: how many items
	MaxLen    *int     `json:"maxlen,omitempty"`
	Source    string   `json:"source,omitempty"`   // the entries are picked from this data source (GET /sources/{source}/search and /items)
	Custom    bool     `json:"custom,omitempty"`   // an id that is not in the source may be added by hand
	Optional  bool     `json:"optional,omitempty"` // one entry or none: the value may be null
}

func (c *core) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", c.serveUI)
	mux.HandleFunc("GET /meta", c.guard(false, c.handleMeta))
	mux.HandleFunc("GET /schema", c.guard(false, c.handleSchema))
	mux.HandleFunc("PUT /values/{key}", c.guard(true, c.handlePut))
	mux.HandleFunc("DELETE /values/{key}", c.guard(true, c.handleDelete))
	mux.HandleFunc("GET /sources/{name}/search", c.guard(false, c.handleSourceSearch))
	mux.HandleFunc("GET /sources/{name}/items", c.guard(false, c.handleSourceItems))
	return mux
}

func (c *core) serveUI(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src https: data:")
	h.Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(c.page()); err != nil {
		c.opts.log.Warn("tunables: write ui", "error", err)
	}
}

// The four places of the page that the server fills in; the page works without them (the script then
// loads everything), but with them it shows the real settings on first paint.
const (
	htmlTitle = `<title>tunables</title>`
	htmlName  = `<span class="bar-title" id="name">tunables</span>`
	htmlHero  = `<h1 class="hero-title" id="heroName">tunables</h1>`
	htmlDesc  = `<p class="hero-desc" id="desc"></p>`
	htmlBoot  = `<script type="application/json" id="boot">null</script>`
)

// page returns the UI with the name, the description and the current values already in it, so the first paint
// needs no request. With a token the page is served bare: it is public, the values are not.
func (c *core) page() []byte {
	if c.opts.token != "" {
		return ui.HTML
	}
	boot, err := json.Marshal(map[string]any{"meta": c.metaPayload(), "schema": c.schemaPayload()}) // escapes < > &
	if err != nil {
		c.opts.log.Warn("tunables: preload the page", "error", err)
		return ui.HTML
	}
	name := html.EscapeString(c.opts.name)
	return []byte(strings.NewReplacer(
		htmlTitle, "<title>"+name+" · tunables</title>",
		htmlName, `<span class="bar-title" id="name">`+name+`</span>`,
		htmlHero, `<h1 class="hero-title" id="heroName">`+name+`</h1>`,
		htmlDesc, `<p class="hero-desc" id="desc">`+html.EscapeString(strings.TrimSpace(c.opts.description))+`</p>`,
		htmlBoot, `<script type="application/json" id="boot">`+string(boot)+`</script>`,
	).Replace(string(ui.HTML)))
}

// guard checks the bearer token and, for writes, the Origin.
func (c *core) guard(write bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c.opts.token != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(c.opts.token)) != 1 {
				c.writeError(w, http.StatusUnauthorized, "missing or wrong token")
				return
			}
		}
		if write {
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || u.Host != r.Host {
					c.writeError(w, http.StatusForbidden, "cross-origin write")
					return
				}
			}
		}
		next(w, r)
	}
}

func (c *core) metaPayload() map[string]any {
	return map[string]any{
		"name":        c.opts.name,
		"description": c.opts.description,
		"protocol":    protocol,
	}
}

func (c *core) schemaPayload() map[string]any {
	s := c.state.Load()
	fields := make([]fieldDTO, 0, len(c.fields))
	for _, f := range c.fields {
		fields = append(fields, c.dto(f, s))
	}
	groups := make([]groupDTO, 0, len(c.groups))
	for _, g := range c.groups {
		groups = append(groups, groupDTO{Key: g.key, Name: g.name, Desc: g.desc, Rule: g.rule, Collapsed: g.collapsed, Feature: g.feature})
	}
	return map[string]any{"groups": groups, "fields": fields}
}

func (c *core) handleMeta(w http.ResponseWriter, _ *http.Request) {
	c.writeJSON(w, http.StatusOK, c.metaPayload())
}

func (c *core) handleSchema(w http.ResponseWriter, _ *http.Request) {
	c.writeJSON(w, http.StatusOK, c.schemaPayload())
}

func (c *core) handlePut(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		c.writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var body struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		c.writeError(w, http.StatusBadRequest, "body must be {\"value\": ...}")
		return
	}
	key := r.PathValue("key")
	c.respondChange(w, key, c.set(key, body.Value))
}

func (c *core) handleDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	c.respondChange(w, key, c.reset(key))
}

func (c *core) respondChange(w http.ResponseWriter, key string, err error) {
	switch {
	case errors.Is(err, ErrUnknownKey):
		c.writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalidValue):
		msg := err.Error()
		var ve *valueError
		if errors.As(err, &ve) {
			msg = ve.reason // "500 is above the maximum 100": the key is already on screen
		}
		c.writeError(w, http.StatusBadRequest, msg)
	case err != nil:
		c.opts.log.Error("tunables: change failed", "key", key, "error", err)
		c.writeError(w, http.StatusInternalServerError, "could not save the change")
	default:
		c.writeJSON(w, http.StatusOK, c.dto(c.byKey[key], c.state.Load()))
	}
}

type groupDTO struct {
	Key  string `json:"key"`
	Name string `json:"name,omitempty"`
	Desc string `json:"desc,omitempty"`
	Rule bool   `json:"rule,omitempty"` // a Rule of a Feature's targeting: its two sets go side by side
	// Collapsed: the page shows the group folded until someone opens it (what they chose is remembered by their browser)
	Collapsed bool `json:"collapsed,omitempty"`
	// Feature: a Feature. The page adds its rules from a list (only those that hold something are shown), and hides them while
	// it is off.
	Feature bool `json:"feature,omitempty"`
}

func (c *core) dto(f *field, s *snapshot) fieldDTO {
	cur := f.read(s.cfg.Elem().FieldByIndex(f.index))
	item := f // the field that holds the enum and the bounds: the item of a list or a set
	d := fieldDTO{MinLen: f.minLen, MaxLen: f.maxLen, Source: f.source, Custom: f.custom, Optional: f.ptr}
	if f.collection() {
		item = f.elem
		d.Item = item.apiType()
	}
	d.Key, d.Name, d.Type, d.UI, d.Desc = f.key, f.name, f.apiType(), f.ui, f.desc
	d.Precision, d.Enum, d.Min, d.Max = item.prec, item.enum, item.bound(item.min), item.bound(item.max)
	d.Default, d.Value, d.Modified = jsonValue(f.def), jsonValue(cur), !f.same(cur, f.def)
	return d
}

func (c *core) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		c.opts.log.Warn("tunables: write response", "error", err)
	}
}

func (c *core) writeError(w http.ResponseWriter, status int, msg string) {
	c.writeJSON(w, status, map[string]string{"error": msg})
}

// handleSourceSearch asks a data source for the entries that match what was typed.
func (c *core) handleSourceSearch(w http.ResponseWriter, r *http.Request) {
	src, ok := c.sources[r.PathValue("name")]
	if !ok {
		c.writeError(w, http.StatusNotFound, "unknown source")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if rs := []rune(q); len(rs) > sourceMaxQuery {
		q = string(rs[:sourceMaxQuery])
	}
	limit, offset := sourceDefLimit, 0
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			limit = min(max(n, 1), sourceMaxLimit)
		}
	}
	if s := r.URL.Query().Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			offset = min(max(n, 0), sourceMaxOffset)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), sourceTimeout)
	defer cancel()
	// one more than the page: that is how it is known whether there is a next one
	items, err := src.search(ctx, q, limit+1, offset)
	if err != nil {
		c.opts.log.Error("tunables: source search failed", "source", src.name, "error", err)
		c.writeError(w, http.StatusBadGateway, "the source did not answer")
		return
	}
	more := len(items) > limit
	c.writeJSON(w, http.StatusOK, map[string]any{"items": cut(items, limit), "more": more})
}

// handleSourceItems finds the entries of the ids a setting holds, to show them by name and picture.
func (c *core) handleSourceItems(w http.ResponseWriter, r *http.Request) {
	src, ok := c.sources[r.PathValue("name")]
	if !ok {
		c.writeError(w, http.StatusNotFound, "unknown source")
		return
	}
	texts := r.URL.Query()["id"]
	if len(texts) > sourceMaxItemIDs {
		c.writeError(w, http.StatusBadRequest, "too many ids")
		return
	}
	ids := make([]any, 0, len(texts))
	for _, t := range texts {
		id, err := src.parseID(t)
		if err != nil {
			c.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		c.writeJSON(w, http.StatusOK, map[string]any{"items": []sourceItem{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sourceTimeout)
	defer cancel()
	items, err := src.get(ctx, ids)
	if err != nil {
		c.opts.log.Error("tunables: source get failed", "source", src.name, "error", err)
		c.writeError(w, http.StatusBadGateway, "the source did not answer")
		return
	}
	c.writeJSON(w, http.StatusOK, map[string]any{"items": cut(items, len(ids))})
}

// cut keeps at most n entries of what a data source returned (never nil: the page reads an array).
func cut(items []sourceItem, n int) []sourceItem {
	if items == nil {
		return []sourceItem{}
	}
	return items[:min(len(items), n)]
}
