package main

import "testing"

func TestHeuristicIntentParsesPersianBudgetAndPriorities(t *testing.T) {
	intent := heuristicIntent("یه ماشین پرآپشن بین ۲ تا ۴ میلیارد بدون رنگ برای شهر میخوام")
	if intent.MinPriceIRR != 20000000000 || intent.MaxPriceIRR != 40000000000 {
		t.Fatalf("unexpected budget: %#v", intent)
	}
	if len(intent.Priorities) != 1 || intent.Priorities[0] != "options" || intent.HardConstraints[0] != "no_paint" {
		t.Fatalf("unexpected criteria: %#v", intent)
	}
}

func TestScoreListingRewardsRelevantCriteria(t *testing.T) {
	intent := Intent{MaxPriceIRR: 40000000000, Priorities: []string{"options", "space"}}
	best := Listing{Brand: "چری", Model: "X55 Pro", PriceIRR: 32000000000, MileageKM: 28000, Transmission: "اتومات", BodyCondition: "بدون رنگ", Features: []string{"spacious", "premium", "family"}}
	weak := Listing{Brand: "سایپا", Model: "شاهین", PriceIRR: 39000000000, MileageKM: 180000, Transmission: "دنده‌ای", BodyCondition: "کارشناسی‌شده"}
	bestScore, reasons, _ := scoreListing(best, intent)
	weakScore, _, cautions := scoreListing(weak, intent)
	if bestScore <= weakScore || len(reasons) < 3 || len(cautions) == 0 {
		t.Fatalf("ranking did not reflect criteria: best=%v weak=%v reasons=%v cautions=%v", bestScore, weakScore, reasons, cautions)
	}
}

func TestSearchTokensKeepsDistinctTerms(t *testing.T) {
	tokens := searchTokens("سمند، مشکی سمند")
	if len(tokens) != 2 || tokens[0] != "سمند" || tokens[1] != "مشکی" {
		t.Fatalf("unexpected search tokens: %#v", tokens)
	}
}

func TestHeuristicIntentCapturesColorOriginAndCompact(t *testing.T) {
	intent := heuristicIntent("ماشین چینی کوچک و کم‌مصرف با رنگ مشکی بین ۱۲ تا ۱۴ میلیارد")
	if intent.MinPriceIRR != 120000000000 || intent.MaxPriceIRR != 140000000000 {
		t.Fatalf("unexpected range: %#v", intent)
	}
	if len(intent.Origins) != 1 || intent.Origins[0] != "chinese" || len(intent.Colors) != 1 || intent.Colors[0] != "مشکی" {
		t.Fatalf("unexpected semantic filters: %#v", intent)
	}
	if !containsAny(intent.Priorities, "compact") || !containsAny(intent.Priorities, "low_fuel") {
		t.Fatalf("missing compact/low fuel priorities: %#v", intent.Priorities)
	}
}

func TestIntentMatchingTreatsColorAndLuxuryAsHardConstraints(t *testing.T) {
	intent := Intent{Colors: []string{"مشکی"}, Priorities: []string{"luxury"}}
	if matchesIntent(Listing{Brand: "کیا", Color: "مشکی"}, intent) {
		t.Fatal("a non-luxury listing should not satisfy luxury intent")
	}
	if !matchesIntent(Listing{Brand: "بی ام و", Model: "X3", Color: "مشکی"}, intent) {
		t.Fatal("a black BMW should satisfy luxury intent")
	}
}

func TestHeuristicIntentCapturesVehicleRelations(t *testing.T) {
	intent := heuristicIntent("ماشین سدان راحت برای جاده، اتومات، مدل ۱۳۹۸ تا ۱۴۰۲، کارکرد زیر ۱۰۰ هزار و بی رنگ")
	if intent.MinYear != 1398 || intent.MaxYear != 1402 || intent.MaxMileageKM != 100000 {
		t.Fatalf("unexpected year/mileage: %#v", intent)
	}
	if intent.Transmission != "automatic" || !containsAny(intent.BodyTypes, "sedan") || !containsAny(intent.Priorities, "comfort") {
		t.Fatalf("unexpected vehicle relations: %#v", intent)
	}
	if !containsAny(intent.BodyConditions, "no_paint") {
		t.Fatalf("missing body condition: %#v", intent.BodyConditions)
	}
}

func TestEconomicLuxuryConflictIsExplicit(t *testing.T) {
	intent := enrichIntent(Intent{}, "ماشین لوکس اقتصادی")
	if len(intent.Conflicts) == 0 || intent.MinPriceIRR != 90000000000 || intent.MaxPriceIRR != 50000000000 {
		t.Fatalf("unexpected conflict normalization: %#v", intent)
	}
}

func TestHeuristicIntentRespectsNegations(t *testing.T) {
	intent := heuristicIntent("خودروی غیرلوکس و غیراقتصادی بدون آپشن")
	if !containsAny(intent.Priorities, "non_luxury") || !containsAny(intent.Priorities, "non_economic") || !containsAny(intent.Priorities, "no_options") {
		t.Fatalf("negations were not captured: %#v", intent.Priorities)
	}
	for _, value := range intent.Priorities {
		if value == "luxury" || value == "economic" || value == "options" {
			t.Fatalf("positive criteria leaked through negations: %#v", intent.Priorities)
		}
	}
}
