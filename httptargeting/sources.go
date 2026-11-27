package httptargeting

import (
	"strings"

	useragent "github.com/mileusna/useragent"
	tunables "github.com/nikulex/gotunables"
)

// The lists the rules of Targeting pick from, registered when the package is imported: a field refers to them as
// `tune:"source:browsers"` and the program sets nothing up. A source of the program's own under the same name
// (tunables.WithSource) replaces one of them, which is how to change a list or to follow another User-Agent parser.
//
//	browsers, systems  the names github.com/mileusna/useragent knows; the parser names others too: the rules take a name
//	           that is not in the list (custom)
//	devices    mobile, tablet, desktop, bot
//
//	countries, languages, regions, cities  empty: the lists of the places and of the languages come from the integrations,
//	           each a module of its own so that this one holds only the User-Agent parser. Type the values (custom) or add one:
//	           httptargeting/geo/mmdb: all four of the geo database file you have (the country, region and city of an address too)
//	           httptargeting/geo/iso:  the countries and the languages by the ISO lists
//	           or your own source under these names (tunables.WithSource).
func init() {
	for _, name := range []string{"countries", "languages", "regions", "cities"} {
		tunables.RegisterSource(name, tunables.StaticSource[string](nil))
	}
	tunables.RegisterSource("browsers", Browsers())
	tunables.RegisterSource("systems", Systems())
	tunables.RegisterSource("devices", Devices())
}

// The lists as sources, to use as they are, to add to (tunables.CombineSources(httptargeting.Browsers(), mine)) or to
// replace (tunables.WithSource("browsers", mine)).

// Browsers is the list of the browsers the User-Agent parser knows.
func Browsers() tunables.DataSource[string] {
	return tunables.StaticSource(namedList(
		useragent.Chrome, useragent.Firefox, useragent.Safari, useragent.Edge, useragent.Opera, useragent.OperaMini, useragent.OperaTouch,
		useragent.Vivaldi, useragent.SamsungBrowser, useragent.InternetExplorer, useragent.HeadlessChrome,
		useragent.FacebookApp, useragent.InstagramApp, useragent.TiktokApp))
}

// Systems is the list of the operating systems the User-Agent parser knows.
func Systems() tunables.DataSource[string] {
	return tunables.StaticSource(namedList(
		useragent.Windows, useragent.WindowsPhone, useragent.MacOS, useragent.IOS, useragent.Android, useragent.Linux,
		useragent.ChromeOS, useragent.FreeBSD, useragent.BlackBerry, useragent.Harmony))
}

// Devices is the list of the kinds of device: mobile, tablet, desktop, bot.
func Devices() tunables.DataSource[string] {
	return tunables.StaticSource(namedList("Mobile", "Tablet", "Desktop", "Bot"))
}

// namedList makes a list of names: the id is the name in lower case, which is how the parser's values are put.
func namedList(names ...string) []tunables.Item[string] {
	out := make([]tunables.Item[string], len(names))
	for i, n := range names {
		out[i] = tunables.Item[string]{ID: strings.ToLower(n), Name: n}
	}
	return out
}
