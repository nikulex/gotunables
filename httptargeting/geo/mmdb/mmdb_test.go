package mmdb

import (
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
	tunables "github.com/nikulex/gotunables"
	"github.com/nikulex/gotunables/httptargeting"
)

func entry(country, region, city string) mmdbtype.Map {
	names := func(n string) mmdbtype.Map { return mmdbtype.Map{"names": mmdbtype.Map{"en": mmdbtype.String(n)}} }
	countryNames := map[string]string{"RS": "Serbia", "LK": "Sri Lanka", "FR": "France"}
	m := mmdbtype.Map{"country": mmdbtype.Map{"iso_code": mmdbtype.String(country), "names": mmdbtype.Map{"en": mmdbtype.String(countryNames[country])}}}
	if region != "" {
		m["subdivisions"] = mmdbtype.Slice{names(region)}
	}
	if city != "" {
		m["city"] = names(city)
	}
	return m
}

// open makes a small database file: the same entry for two networks, a region two countries have, an entry without a city.
func open(t *testing.T) *Database {
	t.Helper()
	w, err := mmdbwriter.New(mmdbwriter.Options{DatabaseType: "City", RecordSize: 28})
	if err != nil {
		t.Fatal(err)
	}
	for cidr, e := range map[string]mmdbtype.Map{
		"81.0.0.0/8": entry("RS", "Vojvodina", "Novi Sad"),
		"82.0.0.0/8": entry("RS", "Vojvodina", "Novi Sad"),
		"5.0.0.0/8":  entry("LK", "Central", "Kandy"),
		"6.0.0.0/8":  entry("RS", "Central", "Nis"),
		"7.0.0.0/8":  entry("FR", "", ""),
	} {
		_, n, _ := netParse(cidr)
		if err := w.Insert(n, e); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "city.mmdb")
	f, err := createFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func names(items []tunables.Item[string]) (out []string) {
	for _, it := range items {
		out = append(out, it.ID+"="+it.Name)
	}
	return
}

func TestLocate(t *testing.T) {
	db := open(t)
	if g := db.Locate(netip.MustParseAddr("81.2.3.4")); g != (httptargeting.Geo{Country: "RS", Region: "Vojvodina", City: "Novi Sad"}) {
		t.Errorf("got %+v", g)
	}
	if g := db.Locate(netip.MustParseAddr("::ffff:5.1.1.1")); g.City != "Kandy" {
		t.Errorf("an IPv4 address in IPv6 form: %+v", g)
	}
	if g := db.Locate(netip.MustParseAddr("7.1.1.1")); g != (httptargeting.Geo{Country: "FR"}) {
		t.Errorf("no region, no city: %+v", g)
	}
	if g := db.Locate(netip.MustParseAddr("100.1.1.1")); g != (httptargeting.Geo{}) {
		t.Errorf("not in the database: %+v", g)
	}
	if g := db.Locate(netip.Addr{}); g != (httptargeting.Geo{}) {
		t.Errorf("not an address: %+v", g)
	}
	var nilDB *Database
	if g := nilDB.Locate(netip.MustParseAddr("81.2.3.4")); g != (httptargeting.Geo{}) {
		t.Errorf("no database: %+v", g)
	}
}

func TestTheListsComeFromTheFile(t *testing.T) {
	db, ctx := open(t), context.Background()
	got, err := db.Regions().Search(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"central=Central (LK, RS)", "vojvodina=Vojvodina (RS)"}; !equal(names(got), want) {
		t.Errorf("regions %v, want %v", names(got), want)
	}
	got, err = db.Cities().Search(ctx, "ni", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nis=Nis (RS)"}; !equal(names(got), want) {
		t.Errorf("cities for ni: %v, want %v", names(got), want)
	}
	all, err := db.Cities().Search(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"kandy=Kandy (LK)", "nis=Nis (RS)", "novi sad=Novi Sad (RS)"}; !equal(names(all), want) {
		t.Errorf("cities %v, want %v", names(all), want)
	}
	countries, err := db.Countries().Search(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"fr=France", "rs=Serbia", "lk=Sri Lanka"}; !equal(names(countries), want) {
		t.Errorf("countries %v, want %v", names(countries), want)
	}
	byID, err := db.Cities().Get(ctx, []string{"novi sad", "nowhere"})
	if err != nil || len(byID) != 1 || byID[0].Name != "Novi Sad (RS)" {
		t.Errorf("get: %v, %v", names(byID), err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The sources of the database stand for the lists of the rules of Targeting.
func TestTheRulesTakeTheSources(t *testing.T) {
	type cfg struct {
		Feature tunables.Feature[httptargeting.Targeting] `json:"feature" tune:"name:Feature"`
	}
	if _, err := tunables.New(cfg{}, open(t).Sources()...); err != nil {
		t.Fatal(err)
	}
}

func netParse(s string) (string, *net.IPNet, error) {
	ip, n, err := net.ParseCIDR(s)
	return ip.String(), n, err
}

func createFile(path string) (*os.File, error) { return os.Create(path) }
