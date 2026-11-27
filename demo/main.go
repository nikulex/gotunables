// Demo: `go run ./demo`, open http://127.0.0.1:6767/ and change a value; the terminal prints the new config.
package main

import (
	"log"
	"net/http"
	"reflect"
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

	log.Println("settings UI: http://127.0.0.1:6767/")
	log.Fatal(http.ListenAndServe("127.0.0.1:6767", t.Handler()))
}
