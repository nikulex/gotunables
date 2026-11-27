// Package mmdb is the place of an address and the lists of the regions and the cities, from a city database in the MaxMind
// DB format: GeoLite2-City, DB-IP City Lite, or any file made the same way. It is the GeoLocator of httptargeting and the
// source of its "countries", "regions" and "cities" lists, all from one file: a program needs no other list of the places
// (httptargeting/geo/iso has the countries by ISO, for one that has no database).
//
//	db, err := mmdb.Open("dbip-city-lite.mmdb")
//	if err != nil { ... }
//	defer db.Close()
//
//	parser := httptargeting.NewParser(httptargeting.WithGeo(db))              // the place of the request's address
//	t, err := tunables.New(cfg, db.Sources()...)                              // the lists the rules of the page pick from
//
// The names are the English ones, in lower case as the parser gives them: the region is the first (the largest) of the
// subdivisions of the city.
package mmdb

import (
	"context"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"sync"

	tunables "github.com/nikulex/gotunables"
	"github.com/nikulex/gotunables/httptargeting"
	"github.com/oschwald/maxminddb-golang/v2"
)

// Database is an open file of the database. It is safe for use from many goroutines.
type Database struct {
	r *maxminddb.Reader

	mu        sync.Mutex // the lists are made when they are first asked for, once
	countries tunables.DataSource[string]
	regions   tunables.DataSource[string]
	cities    tunables.DataSource[string]
}

// Open opens the file of the database.
func Open(path string) (*Database, error) {
	r, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("mmdb: %w", err)
	}
	return &Database{r: r}, nil
}

// Close closes the file. The database is not for use after it.
func (d *Database) Close() error { return d.r.Close() }

// record is the part of an entry of the database that is read: the rest is skipped.
type record struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
		Names   struct {
			English string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"country"`
	City struct {
		Names struct {
			English string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"city"`
	Subdivisions []struct {
		Names struct {
			English string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
}

func (r *record) geo() httptargeting.Geo {
	g := httptargeting.Geo{Country: r.Country.ISOCode, City: r.City.Names.English}
	if len(r.Subdivisions) > 0 {
		g.Region = r.Subdivisions[0].Names.English
	}
	return g
}

// Locate says where an address is (it is a httptargeting.GeoLocator): the country, the region and the city, as far as the
// database knows; an address it does not know is an empty Geo.
func (d *Database) Locate(ip netip.Addr) httptargeting.Geo {
	if d == nil || !ip.IsValid() {
		return httptargeting.Geo{}
	}
	var rec record
	if err := d.r.Lookup(ip.Unmap()).Decode(&rec); err != nil {
		return httptargeting.Geo{}
	}
	return rec.geo()
}

// Countries is the list of the countries of the database, by the English names: the ids are the codes in lower case ("rs").
// The first call reads the whole file (see Regions).
func (d *Database) Countries() tunables.DataSource[string] {
	return d.source(func() tunables.DataSource[string] { return d.countries })
}

// Regions is the list of the regions of the database, to pick from on the page: a region is named once, with the countries
// that have one by that name ("Central (RS, LK)"). The first call reads the whole file (a few seconds for a database of the
// world), and the lists are kept.
func (d *Database) Regions() tunables.DataSource[string] {
	return d.source(func() tunables.DataSource[string] { return d.regions })
}

// Cities is the list of the cities of the database (see Regions).
func (d *Database) Cities() tunables.DataSource[string] {
	return d.source(func() tunables.DataSource[string] { return d.cities })
}

// Sources are the options that give the rules their lists (countries, regions, cities): tunables.New(cfg, db.Sources()...).
func (d *Database) Sources() []tunables.Option {
	return []tunables.Option{
		tunables.WithSource("countries", d.Countries()),
		tunables.WithSource("regions", d.Regions()),
		tunables.WithSource("cities", d.Cities()),
	}
}

// source is a data source that reads the file when it is first used.
func (d *Database) source(list func() tunables.DataSource[string]) tunables.DataSource[string] {
	return tunables.DataSource[string]{
		Search: func(ctx context.Context, query string, limit, offset int) ([]tunables.Item[string], error) {
			if err := d.load(); err != nil {
				return nil, err
			}
			return list().Search(ctx, query, limit, offset)
		},
		Get: func(ctx context.Context, ids []string) ([]tunables.Item[string], error) {
			if err := d.load(); err != nil {
				return nil, err
			}
			return list().Get(ctx, ids)
		},
	}
}

// load reads the regions and the cities of the whole file, once (a failure is tried again the next time).
func (d *Database) load() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.countries.Search != nil {
		return nil
	}
	countries, regions, cities := map[string]string{}, newPlaces(), newPlaces() // a country: its code, its name
	seen := map[uintptr]bool{}                                                  // many networks share one entry: it is read once
	for res := range d.r.Networks() {
		if err := res.Err(); err != nil {
			return fmt.Errorf("mmdb: %w", err)
		}
		if seen[res.Offset()] {
			continue
		}
		seen[res.Offset()] = true
		var rec record
		if err := res.Decode(&rec); err != nil {
			return fmt.Errorf("mmdb: %w", err)
		}
		g := rec.geo()
		if code := strings.ToLower(g.Country); code != "" {
			countries[code] = rec.Country.Names.English
		}
		regions.add(g.Region, g.Country)
		cities.add(g.City, g.Country)
	}
	d.countries, d.regions, d.cities = tunables.StaticSource(countryItems(countries)), tunables.StaticSource(regions.items()), tunables.StaticSource(cities.items())
	return nil
}

// places are names with the countries that have them.
type places map[string]*place

type place struct {
	name      string
	countries map[string]bool
}

func newPlaces() places { return places{} }

func (p places) add(name, country string) {
	if name == "" {
		return
	}
	id := strings.ToLower(name)
	pl := p[id]
	if pl == nil {
		pl = &place{name: name, countries: map[string]bool{}}
		p[id] = pl
	}
	if country != "" {
		pl.countries[country] = true
	}
}

// items are the places by name, in the order of the names; the id is the name in lower case, as the parser gives it.
func (p places) items() []tunables.Item[string] {
	out := make([]tunables.Item[string], 0, len(p))
	for id, pl := range p {
		name := pl.name
		if c := slices.Sorted(maps.Keys(pl.countries)); len(c) > 0 {
			if len(c) > 3 {
				c = append(c[:3], "…")
			}
			name += " (" + strings.Join(c, ", ") + ")"
		}
		out = append(out, tunables.Item[string]{ID: id, Name: name})
	}
	slices.SortFunc(out, func(a, b tunables.Item[string]) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// countryItems are the countries by their names (a code the file has no name for is named by the code).
func countryItems(byCode map[string]string) []tunables.Item[string] {
	out := make([]tunables.Item[string], 0, len(byCode))
	for code, name := range byCode {
		if name == "" {
			name = strings.ToUpper(code)
		}
		out = append(out, tunables.Item[string]{ID: code, Name: name})
	}
	slices.SortFunc(out, func(a, b tunables.Item[string]) int { return strings.Compare(a.Name, b.Name) })
	return out
}
