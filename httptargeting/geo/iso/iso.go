// Package iso is the lists of the countries and the languages of the rules of httptargeting, by the ISO standards: the
// countries by ISO 3166-1 (github.com/biter777/countries), the languages by ISO 639-1 (github.com/emvi/iso-639-1). It is a
// module of its own, so that httptargeting holds only the User-Agent parser; a program that gets its countries from a geo
// database file uses httptargeting/geo/mmdb instead.
//
//	t, err := tunables.New(cfg, iso.Sources()...)
//
// The ids are the codes in lower case ("rs", "sr"), as the parser gives them; the names are the English ones.
package iso

import (
	"slices"
	"strings"

	"github.com/biter777/countries"
	iso6391 "github.com/emvi/iso-639-1"
	tunables "github.com/nikulex/gotunables"
)

// Sources are the options that give the rules their lists: tunables.New(cfg, iso.Sources()...). They stand for the sources
// "countries" and "languages". To add to a list rather than replace it, combine it with your own:
//
//	tunables.WithSource("countries", tunables.CombineSources(iso.Countries(), mine))
func Sources() []tunables.Option {
	return []tunables.Option{
		tunables.WithSource("countries", Countries()),
		tunables.WithSource("languages", Languages()),
	}
}

// Countries is the list of the countries.
func Countries() tunables.DataSource[string] { return tunables.StaticSource(countryList()) }

// Languages is the list of the languages.
func Languages() tunables.DataSource[string] { return tunables.StaticSource(languageList()) }

func countryList() []tunables.Item[string] {
	var out []tunables.Item[string]
	for _, c := range countries.All() {
		if code := strings.ToLower(c.Alpha2()); code != "" {
			out = append(out, tunables.Item[string]{ID: code, Name: c.String()})
		}
	}
	slices.SortFunc(out, func(a, b tunables.Item[string]) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func languageList() []tunables.Item[string] {
	out := make([]tunables.Item[string], 0, len(iso6391.Languages))
	for code, l := range iso6391.Languages {
		out = append(out, tunables.Item[string]{ID: code, Name: l.Name})
	}
	slices.SortFunc(out, func(a, b tunables.Item[string]) int { return strings.Compare(a.Name, b.Name) })
	return out
}
