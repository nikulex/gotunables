// Demo: `go run .` in this directory (it is a module of its own, to use the integrations), open http://127.0.0.1:6767/#/features,
// turn a feature on, add a targeting and Save; the countries are the ISO lists, or those of a geo database file when MMDB=file.mmdb
// is set (then the regions and the cities are its too). Then GET http://127.0.0.1:6767/try (with a header or two: -A "<a User-Agent>" -H "Accept-Language: sr") says what is on for
// the request.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/netip"
	"os"

	tunables "github.com/nikulex/gotunables"
	"github.com/nikulex/gotunables/httptargeting"
	"github.com/nikulex/gotunables/httptargeting/geo/iso"
	"github.com/nikulex/gotunables/httptargeting/geo/mmdb"
)

// MyTargeting adds what only this program knows to the targeting of a web request; MyInfo is what it says about a request, and
// has a field of the same name for each Rule.
type MyTargeting struct {
	httptargeting.Targeting
	Provider tunables.Rule[string] `json:"provider" tune:"name:Account provider; enum:google,telegram,email"`
}

type MyInfo struct {
	httptargeting.Info
	Provider string
}

type Config struct {
	Features struct {
		NewCheckout tunables.Feature[httptargeting.Targeting] `json:"new_checkout" tune:"name:New checkout; desc:The base targeting of a web request"`
		Beta        tunables.Feature[MyTargeting]             `json:"beta" tune:"name:Beta; desc:The same, and the account provider (embedded)"`
	} `json:"features" tune:"name:Features"`
}

func main() {
	// The lists the rules pick from, and where an address is: a geo database file of yours, or the ISO lists and a table of one.
	sources, geo := iso.Sources(), httptargeting.GeoLocator(httptargeting.GeoFunc(func(ip netip.Addr) httptargeting.Geo {
		if ip.IsLoopback() {
			return httptargeting.Geo{Country: "RS", Region: "Vojvodina", City: "Novi Sad"}
		}
		return httptargeting.Geo{}
	}))
	if path := os.Getenv("MMDB"); path != "" {
		db, err := mmdb.Open(path)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
		sources, geo = db.Sources(), db
	}

	t, err := tunables.New(Config{}, append([]tunables.Option{tunables.WithName("httptargeting demo"), tunables.WithDescription("Features that are on for some requests and off for others.")}, sources...)...)
	if err != nil {
		log.Fatal(err)
	}

	// Once, at the start: the targeting and the info are checked against each other, and the checks are made.
	base, err := tunables.NewMatcher[httptargeting.Targeting, httptargeting.Info]()
	if err != nil {
		log.Fatal(err)
	}
	beta, err := tunables.NewMatcher[MyTargeting, MyInfo]()
	if err != nil {
		log.Fatal(err)
	}

	web := httptargeting.NewParser(httptargeting.WithGeo(geo))

	mux := http.NewServeMux()
	mux.Handle("/", t.Handler())
	mux.HandleFunc("GET /try", func(w http.ResponseWriter, r *http.Request) {
		info := web.Parse(r)
		mine := MyInfo{Info: info, Provider: r.URL.Query().Get("provider")}
		f := t.Get().Features
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(map[string]any{
			"info":         mine,
			"new_checkout": base.IsEnabled(&f.NewCheckout, &info),
			"beta":         beta.IsEnabled(&f.Beta, &mine),
		})
		if err != nil {
			log.Println("try:", err)
		}
	})
	log.Println("settings UI: http://127.0.0.1:6767/#/features")
	log.Fatal(http.ListenAndServe("127.0.0.1:6767", mux))
}
