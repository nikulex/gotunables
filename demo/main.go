// Demo: `go run ./demo`, open http://127.0.0.1:6767/ and change a value; the terminal prints the new config.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	tunables "github.com/nikulex/gotunables"
)

// The key of a field comes from its json (or yaml) tag, everything else is one `tune` tag.
// `ui:` picks the component; without it a type has a default (bool: switch, enum: select, number: counter...).
type Features struct {
	Maintenance bool   `json:"maintenance" tune:"name:Maintenance mode; desc:Show the maintenance banner"`
	Banner      string `json:"banner" tune:"ui:textarea; default:We will be back soon"`

	Checkout struct {
		Enabled  bool `json:"enabled" tune:"name:New checkout"`
		MaxItems int  `json:"max_items" tune:"name:Max items; default:10; min:1; max:100; ui:slider"`
		Button   struct {
			Color string `json:"color" tune:"default:blue; enum:blue,green,red"`
			Shape string `json:"shape" tune:"default:round; enum:round,square,pill; ui:buttons"`
			Label string `json:"label" tune:"default:Buy now"`
		} `json:"button" tune:"name:Buy button"`
	} `json:"checkout" tune:"name:Checkout; desc:Cart and payment"`

	Access struct {
		Admins  tunables.Set[int64]  `json:"admins" tune:"desc:Telegram ids of the admins; default:101,205; min:1"`
		Days    tunables.Set[string] `json:"days" tune:"name:Working days; enum:mon,tue,wed,thu,fri,sat,sun; default:mon,tue,wed,thu,fri"`
		Origins []string             `json:"origins" tune:"desc:Allowed origins, in this order; default:https://a.example,https://b.example; maxlen:5"`
		Waits   []time.Duration      `json:"waits" tune:"name:Retry waits; default:1s,5s,30s; min:100ms; max:10m"`
		Ratios  []float32            `json:"ratios" tune:"desc:Weights; min:0; max:1; precision:2; default:0.25,0.5"`
	} `json:"access" tune:"desc:Lists and sets"`

	// The people are picked by name from a data source: the setting holds only their ids.
	People struct {
		Owner     *int              `json:"owner" tune:"source:people; desc:One person, or nobody"`
		Reviewers tunables.Set[int] `json:"reviewers" tune:"source:people; desc:Any number of people; maxlen:6; default:1,3"`
		Cursor    *int              `json:"cursor" tune:"source:people-cursor; desc:The same people, read by a token (a cursor) instead of an offset: how an API that has no offset is paged"`
		Rotation  []int             `json:"rotation" tune:"source:people; custom:true; desc:The same person may come twice, the order matters. An id that is not in the list can be added too"`
	} `json:"people" tune:"name:People; desc:Picked from a data source: the users of dummyjson.com"`

	// A feature is on or off, and when it is on, only for those the targeting lets in. The info of a request says who they are.
	Features struct {
		NewCheckout tunables.Feature[BaseTargeting] `json:"new_checkout" tune:"name:New checkout; desc:The targeting of the program: where from, what language"`
		Beta        tunables.Feature[MyTargeting]   `json:"beta" tune:"name:Beta; desc:The same, and the account provider and the people that only this program knows (embedded)"`
	} `json:"features" tune:"name:Features; desc:Try it: GET /try?country=rs&lang=en&provider=google&user=3 on this address says what is on for you"`

	Retry struct {
		Count  int `yaml:"count" tune:"default:3; min:0; max:10"`
		Policy struct {
			Backoff time.Duration `yaml:"backoff" tune:"default:500ms; min:10ms; max:1m"`
			Jitter  struct {
				Ratio float64 `yaml:"ratio" tune:"default:0.2; min:0; max:1; ui:slider; precision:2"`
			} `yaml:"jitter"`
		} `yaml:"policy"`
	} `yaml:"retry"`
}

// The targeting of a feature is a struct of Rules, and what it is checked against (the info) is a struct with a field of the
// same name for each Rule. The httptargeting package (a module of its own) has the ready ones for a web request.
type BaseTargeting struct {
	Country   tunables.Rule[string] `json:"country" tune:"name:Country; desc:Where from; enum:rs,ua,us,id,de"`
	Languages tunables.Rule[string] `json:"languages" tune:"name:Languages; desc:One of them has to be in; enum:en,sr,uk,ru,de"`
}

// MyTargeting adds what only this program knows.
type MyTargeting struct {
	BaseTargeting
	Provider tunables.Rule[string] `json:"provider" tune:"name:Account provider; enum:google,telegram,email"`
	UserID   tunables.Rule[int]    `json:"users" tune:"name:People; source:people; custom:true"`
}

type BaseInfo struct {
	Country   string
	Languages []string
}

type MyInfo struct {
	BaseInfo
	Provider string
	UserID   int
}

const name = "demo"
const description = `
Live settings of a tiny demo shop: change a value, press Save, and the running program uses it at once, no restart.
The terminal prints the new config every time it changes.
`

func main() {
	t, err := tunables.New(
		Features{},
		tunables.WithName(name),
		tunables.WithDescription(description),
		tunables.WithSource("people", people()),
		tunables.WithSource("people-cursor", byToken(people())),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Your app just reads the struct; here we print it whenever it changes.
	go func() {
		last := *t.Get()
		log.Printf("%+v", last)
		for range time.Tick(time.Second) {
			if c := *t.Get(); !reflect.DeepEqual(c, last) {
				last = c
				log.Printf("%+v", c)
			}
		}
	}()

	// A Matcher is made once for each targeting and the info it is checked against, at the start: it finds out whether they go
	// together (a Rule that finds nothing in the info would silently do nothing), and then checks without reflection.
	base, err := tunables.NewMatcher[BaseTargeting, BaseInfo]()
	if err != nil {
		log.Fatal(err)
	}
	beta, err := tunables.NewMatcher[MyTargeting, MyInfo]()
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", t.Handler())
	// what is on for the one who asks: /try?country=rs&lang=en,sr&provider=google&user=3
	mux.HandleFunc("GET /try", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		info := BaseInfo{Country: q.Get("country")}
		if l := q.Get("lang"); l != "" {
			info.Languages = strings.Split(l, ",")
		}
		mine := MyInfo{BaseInfo: info, Provider: q.Get("provider")}
		mine.UserID, _ = strconv.Atoi(q.Get("user"))
		f := t.Get().Features
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"info":         mine,
			"new_checkout": base.IsEnabled(&f.NewCheckout, &info),
			"beta":         beta.IsEnabled(&f.Beta, &mine),
		})
	})
	log.Println("settings UI: http://127.0.0.1:6767/")
	log.Fatal(http.ListenAndServe("127.0.0.1:6767", mux))
}

// byToken reads a source that takes an offset as one that takes a token, as an API that has no offset would be: the token is an
// opaque text (here it holds the place to go on from, which is all a cursor is) and the page after it is known by the entry that
// is one more than the page.
func byToken(ds tunables.DataSource[int]) tunables.DataSource[int] {
	return tunables.DataSource[int]{
		SearchToken: func(ctx context.Context, query string, limit int, token string) (tunables.Page[int], error) {
			from := 0
			if token != "" {
				var err error
				if from, err = strconv.Atoi(strings.TrimPrefix(token, "cursor-")); err != nil {
					return tunables.Page[int]{}, fmt.Errorf("a token that was not given: %w", err)
				}
			}
			items, err := ds.Search(ctx, query, limit+1, from)
			if err != nil {
				return tunables.Page[int]{}, err
			}
			p := tunables.Page[int]{Items: items[:min(limit, len(items))]}
			if len(items) > limit {
				p.Next = "cursor-" + strconv.Itoa(from+limit)
			}
			return p, nil
		},
		Get: ds.Get,
	}
}

// people is a data source over a public test API (https://dummyjson.com/users: 208 made-up users with pictures).
// Search asks the API's own search; Get takes the entries from the whole list, read once.
func people() tunables.DataSource[int] {
	const base = "https://dummyjson.com/users"
	client := &http.Client{Timeout: 4 * time.Second}
	fetchRaw := func(ctx context.Context, u string) ([]tunables.Item[int], error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("dummyjson: %s", resp.Status)
		}
		var body struct {
			Users []struct {
				ID        int    `json:"id"`
				FirstName string `json:"firstName"`
				LastName  string `json:"lastName"`
				Image     string `json:"image"`
			} `json:"users"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil, err
		}
		items := make([]tunables.Item[int], len(body.Users))
		for i, u := range body.Users {
			items[i] = tunables.Item[int]{ID: u.ID, Name: u.FirstName + " " + u.LastName, ImageSrc: u.Image}
		}
		return items, nil
	}
	// the test API limits how often it is asked (429): an answer is kept for a minute
	type cached struct {
		at    time.Time
		items []tunables.Item[int]
	}
	var cacheMu sync.Mutex
	cache := map[string]cached{}
	fetch := func(ctx context.Context, u string) ([]tunables.Item[int], error) {
		cacheMu.Lock()
		hit, ok := cache[u]
		cacheMu.Unlock()
		if ok && time.Since(hit.at) < time.Minute {
			return hit.items, nil
		}
		items, err := fetchRaw(ctx, u)
		if err != nil {
			return nil, err
		}
		cacheMu.Lock()
		cache[u] = cached{at: time.Now(), items: items}
		cacheMu.Unlock()
		return items, nil
	}
	const fields = "&select=id,firstName,lastName,image"

	var (
		mu  sync.Mutex
		all map[int]tunables.Item[int]
	)
	return tunables.DataSource[int]{
		Search: func(ctx context.Context, query string, limit, offset int) ([]tunables.Item[int], error) {
			if query == "" {
				return fetch(ctx, fmt.Sprintf("%s?limit=%d&skip=%d%s", base, limit, offset, fields))
			}
			return fetch(ctx, fmt.Sprintf("%s/search?q=%s&limit=%d&skip=%d%s", base, url.QueryEscape(query), limit, offset, fields))
		},
		Get: func(ctx context.Context, ids []int) ([]tunables.Item[int], error) {
			mu.Lock()
			defer mu.Unlock()
			if all == nil {
				list, err := fetch(ctx, base+"?limit=0"+fields)
				if err != nil {
					return nil, err
				}
				all = make(map[int]tunables.Item[int], len(list))
				for _, it := range list {
					all[it.ID] = it
				}
			}
			var out []tunables.Item[int]
			for _, id := range ids {
				if it, ok := all[id]; ok {
					out = append(out, it)
				}
			}
			return out, nil
		},
	}
}
