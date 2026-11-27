package tunables

import (
	"fmt"
	"strings"
	"testing"
)

type keyedInfo struct {
	Country   string
	Languages []string
	Provider  string
	UserID    uint
	Groups    []string
	Session   string `target:"key"`
}

type keyedByNumber struct {
	Country   string
	Languages []string
	Provider  string
	UserID    uint `target:"key"`
	Groups    []string
}

func percent(n int) *int { return &n }

func TestMurmurHashIsTheStandardOne(t *testing.T) {
	for _, tc := range []struct {
		in   string
		seed uint32
		want uint32
	}{
		{"", 0, 0},
		{"", 1, 0x514e28b7},
		{"", 0xffffffff, 0x81f16f39},
		{"test", 0, 0xba6bd213},
		{"Hello, world!", 0, 0xc0363e43},
		{"The quick brown fox jumps over the lazy dog", 0x9747b28c, 0x2fa826cd},
		{"abc", 0, 0xb3dd93fa},
		{"ab", 0, 0x9bbfd75f},
		{"a", 0, 0x3c2569b2},
	} {
		if got := murmur32(tc.in, tc.seed); got != tc.want {
			t.Errorf("murmur32(%q, %#x) = %#x, want %#x", tc.in, tc.seed, got, tc.want)
		}
		if got := murmur32([]byte(tc.in), tc.seed); got != tc.want {
			t.Errorf("murmur32 of bytes (%q) = %#x, want %#x", tc.in, got, tc.want)
		}
	}
}

func feature10() *Feature[siteTargeting] {
	return &Feature[siteTargeting]{Enabled: true, Groups: []Group[siteTargeting]{{ID: "ten", Percent: percent(10)}}}
}

func TestAShareOfTheTrafficIsCutByTheKey(t *testing.T) {
	f := feature10()
	in := 0
	const users = 20000
	for i := 0; i < users; i++ {
		info := keyedInfo{Session: fmt.Sprintf("user-%d", i)}
		first := f.IsEnabled(info)
		for j := 0; j < 3; j++ {
			if f.IsEnabled(info) != first {
				t.Fatalf("user-%d falls on one side, then on the other", i)
			}
		}
		if first {
			in++
		}
	}
	if in < users*8/100 || in > users*12/100 {
		t.Errorf("10%% of %d is about %d, got %d", users, users/10, in)
	}
}

func TestAGrowingShareOnlyTakesPeopleIn(t *testing.T) {
	f := feature10()
	was := map[string]bool{}
	for i := 0; i < 5000; i++ {
		k := fmt.Sprintf("u%d", i)
		was[k] = f.IsEnabled(keyedInfo{Session: k})
	}
	f.Groups[0].Percent = percent(30)
	for k, in := range was {
		if in && !f.IsEnabled(keyedInfo{Session: k}) {
			t.Fatalf("%s was in the 10%% and is out of the 30%%", k)
		}
	}
}

func TestTheSharesOfTwoGroupsAreNotTheSamePeople(t *testing.T) {
	a := &Feature[siteTargeting]{Enabled: true, Groups: []Group[siteTargeting]{{ID: "a", Percent: percent(10)}}}
	b := &Feature[siteTargeting]{Enabled: true, Groups: []Group[siteTargeting]{{ID: "b", Percent: percent(10)}}}
	both := 0
	for i := 0; i < 20000; i++ {
		info := keyedInfo{Session: fmt.Sprintf("u%d", i)}
		if a.IsEnabled(info) && b.IsEnabled(info) {
			both++
		}
	}
	if both > 20000*3/100 { // 1% if they are independent, 10% if they are the same people
		t.Errorf("%d are in both", both)
	}
}

func TestTheEdgesOfAShare(t *testing.T) {
	f := feature10()
	info := keyedInfo{Session: "someone"}
	f.Groups[0].Percent = percent(0)
	for i := 0; i < 50; i++ {
		if f.IsEnabled(info) {
			t.Fatal("0%: nobody")
		}
	}
	f.Groups[0].Percent = percent(100)
	f.Groups[0].Targeting.Country.Include = NewSet("rs")
	if !f.IsEnabled(keyedInfo{Country: "rs"}) || f.IsEnabled(keyedInfo{Country: "us"}) {
		t.Error("100%: all of those the targeting lets in")
	}
	f.Groups[0].Percent = nil
	if !f.IsEnabled(keyedInfo{Country: "rs"}) {
		t.Error("no percent: all")
	}
	// the share is of those the targeting lets in, not of everybody
	f.Groups[0].Percent = percent(50)
	for i := 0; i < 300; i++ {
		if f.IsEnabled(keyedInfo{Country: "us", Session: fmt.Sprint(i)}) {
			t.Fatal("a share does not let in who the rules keep out")
		}
	}
}

func TestAnyGroupWithAShareThatTakesInLetsIn(t *testing.T) {
	f := &Feature[siteTargeting]{Enabled: true, Groups: []Group[siteTargeting]{
		{ID: "none", Percent: percent(0)},
		{ID: "serbia", Targeting: siteTargeting{Country: Rule[string]{Include: NewSet("rs")}}},
	}}
	if !f.IsEnabled(keyedInfo{Country: "rs", Session: "x"}) {
		t.Error("the second group lets Serbia in whatever the first one does")
	}
	if f.IsEnabled(keyedInfo{Country: "us", Session: "x"}) {
		t.Error("and nobody else")
	}
}

func TestWithNoKeyOrRandomTheShareIsTheLuckOfACheck(t *testing.T) {
	f := feature10()
	f.Groups[0].Percent = percent(50)
	count := func(info any) int {
		in := 0
		for i := 0; i < 4000; i++ {
			if f.IsEnabled(info) {
				in++
			}
		}
		return in
	}
	for name, info := range map[string]any{"no key in the info": siteInfo{}, "a key that is empty": keyedInfo{}, "nothing known": nil} {
		if n := count(info); n < 1700 || n > 2300 {
			t.Errorf("%s: %d of 4000 at 50%%", name, n)
		}
	}
	f.Groups[0].Random = true
	same := keyedInfo{Session: "the same"}
	if n := count(same); n < 1700 || n > 2300 {
		t.Errorf("random, with a key: %d of 4000 at 50%%, it must not follow the key", n)
	}
}

func TestAMatcherCutsTheSameShareAsTheFeature(t *testing.T) {
	m, err := NewMatcher[siteTargeting, keyedInfo]()
	if err != nil {
		t.Fatal(err)
	}
	f := feature10()
	f.Groups[0].Percent = percent(40)
	for i := 0; i < 3000; i++ {
		info := keyedInfo{Session: fmt.Sprintf("u%d", i)}
		if m.Targets(f, &info) != f.Targets(info) {
			t.Fatalf("the matcher and the feature disagree on u%d", i)
		}
	}
	nm, err := NewMatcher[siteTargeting, keyedByNumber]()
	if err != nil {
		t.Fatal(err)
	}
	for i := uint(1); i < 3000; i++ {
		info := keyedByNumber{UserID: i}
		if nm.Targets(f, &info) != f.Targets(info) || nm.Targets(f, &info) != f.Targets(&info) {
			t.Fatalf("a number as the key: %d", i)
		}
	}
	var in int
	for i := uint(1); i < 3000; i++ {
		if nm.IsEnabled(f, &keyedByNumber{UserID: i}) {
			in++
		}
	}
	if in < 1000 || in > 1400 {
		t.Errorf("40%% of 3000: %d", in)
	}
}

func TestAMatcherWithAShareAllocatesNothing(t *testing.T) {
	m, err := NewMatcher[siteTargeting, keyedInfo]()
	if err != nil {
		t.Fatal(err)
	}
	f := feature10()
	info := keyedInfo{Session: "someone-with-a-long-enough-key-to-be-hashed"}
	if n := testing.AllocsPerRun(200, func() { m.Targets(f, &info) }); n != 0 {
		t.Errorf("%v allocations for a check", n)
	}
	nm, err := NewMatcher[siteTargeting, keyedByNumber]()
	if err != nil {
		t.Fatal(err)
	}
	byNumber := keyedByNumber{UserID: 77}
	if n := testing.AllocsPerRun(200, func() { nm.Targets(f, &byNumber) }); n != 0 {
		t.Errorf("%v allocations for a check by a number", n)
	}
}

func TestTheKeyOfTheInfoIsChecked(t *testing.T) {
	if err := CheckInfo[siteTargeting, keyedInfo](); err != nil {
		t.Errorf("a key needs no Rule of its own: %v", err)
	}
	if err := CheckInfo[siteTargeting, keyedByNumber](); err != nil {
		t.Errorf("a key may have a Rule as any field: %v", err)
	}
	type two struct {
		keyedInfo
		Other string `target:"key"`
	}
	if err := CheckInfo[siteTargeting, two](); err == nil || !strings.Contains(err.Error(), "only one is the key") {
		t.Errorf("two keys: %v", err)
	}
	type float struct {
		fullInfo
		Score float64 `target:"key"`
	}
	if err := CheckInfo[siteTargeting, float](); err == nil || !strings.Contains(err.Error(), "is the key") {
		t.Errorf("a key that is not a text or a number: %v", err)
	}
}

func TestAPercentIsCheckedAndKept(t *testing.T) {
	tn, err := New(groupsConfig{}, WithName("t"), usersFiveSource())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []int{-1, 101} {
		if err := tn.Set("checkout.groups", []Group[siteTargeting]{{Percent: percent(p)}}); err == nil {
			t.Errorf("%d%% is not a share", p)
		}
	}
	if err := tn.Set("checkout.groups", []Group[siteTargeting]{{ID: "a", Percent: percent(100)}, {ID: "b", Percent: percent(0)}, {ID: "c", Percent: percent(25), Random: true}}); err != nil {
		t.Fatal(err)
	}
	gs := tn.Get().Checkout.Groups
	if gs[0].Percent != nil {
		t.Error("100% is all: kept as nothing")
	}
	if gs[1].Percent == nil || *gs[1].Percent != 0 {
		t.Errorf("0%% is kept: %+v", gs[1].Percent)
	}
	if gs[2].Percent == nil || *gs[2].Percent != 25 || !gs[2].Random {
		t.Errorf("%+v", gs[2])
	}
}
