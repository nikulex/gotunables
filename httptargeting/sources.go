package httptargeting

import (
	"slices"
	"strings"

	"github.com/biter777/countries"
	iso6391 "github.com/emvi/iso-639-1"
	useragent "github.com/mileusna/useragent"
	tunables "github.com/nikulex/gotunables"
)

// The lists the rules of Targeting pick from, registered when the package is imported: a field refers to them as
// `tune:"source:countries"` and the program sets nothing up. A source of the program's own under the same name
// (tunables.WithSource) replaces one of them, which is how to change a list or to follow another User-Agent parser.
//
//	countries  ISO 3166-1 codes in lower case ("rs"), by their English names (github.com/biter777/countries)
//	languages  ISO 639-1 codes ("sr"), by their English names (github.com/emvi/iso-639-1)
//	browsers, systems  the names github.com/mileusna/useragent knows; the parser names others too: the rules take a name
//	           that is not in the list (custom)
//	devices    mobile, tablet, desktop, bot
//
// What a geo database calls a region or a city is not the same in each one, so there are no lists of them: type them, or
// register your own source for the database you use.
func init() {
	tunables.RegisterSource("countries", tunables.StaticSource(countryList()))
	tunables.RegisterSource("languages", tunables.StaticSource(languageList()))
	tunables.RegisterSource("browsers", tunables.StaticSource(namedList(
		useragent.Chrome, useragent.Firefox, useragent.Safari, useragent.Edge, useragent.Opera, useragent.OperaMini, useragent.OperaTouch,
		useragent.Vivaldi, useragent.SamsungBrowser, useragent.InternetExplorer, useragent.HeadlessChrome,
		useragent.FacebookApp, useragent.InstagramApp, useragent.TiktokApp)))
	tunables.RegisterSource("systems", tunables.StaticSource(namedList(
		useragent.Windows, useragent.WindowsPhone, useragent.MacOS, useragent.IOS, useragent.Android, useragent.Linux,
		useragent.ChromeOS, useragent.FreeBSD, useragent.BlackBerry, useragent.Harmony)))
	tunables.RegisterSource("devices", tunables.StaticSource(namedList("Mobile", "Tablet", "Desktop", "Bot")))
}

// namedList makes a list of names: the id is the name in lower case, which is how the parser's values are put.
func namedList(names ...string) []tunables.Item[string] {
	out := make([]tunables.Item[string], len(names))
	for i, n := range names {
		out[i] = tunables.Item[string]{ID: strings.ToLower(n), Name: n}
	}
	return out
}

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
