package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIResponsesURLUsesCustomBaseURL(t *testing.T) {
	for _, test := range []struct {
		baseURL string
		want    string
	}{
		{baseURL: "https://api.example.com/v1", want: "https://api.example.com/v1/responses"},
		{baseURL: "https://api.example.com/v1/", want: "https://api.example.com/v1/responses"},
		{baseURL: "", want: "https://api.openai.com/v1/responses"},
	} {
		ai := OpenAI{BaseURL: test.baseURL}
		if got := ai.responsesURL(); got != test.want {
			t.Fatalf("base URL %q: got %q, want %q", test.baseURL, got, test.want)
		}
	}
}

func TestResponseTextExtractsJSONFromMessageOutput(t *testing.T) {
	raw := []byte(`{"output":[{"type":"reasoning","content":[]},{"type":"message","content":[{"type":"output_text","text":"` + "```json\\n{\\\"city\\\":\\\"تهران\\\"}\\n```" + `"}]}]}`)
	got, err := responseText(raw)
	if err != nil || got != `{"city":"تهران"}` {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestExtractIntentUsesResponsesAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/responses" || body["input"] != "ماشین در تهران" || body["instructions"] == nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"output_text":"{\"city\":\"تهران\"}"}`))
	}))
	defer server.Close()
	ai := OpenAI{Key: "test", Model: "test-model", BaseURL: server.URL + "/v1"}
	intent, err := ai.ExtractIntent(context.Background(), "ماشین در تهران")
	if err != nil || intent.City != "تهران" {
		t.Fatalf("got %+v, %v", intent, err)
	}
}

func TestExtractIntentErrorsWithoutKeySoCallerFallsBack(t *testing.T) {
	if _, err := (&OpenAI{}).ExtractIntent(context.Background(), "پراید"); err == nil {
		t.Fatal("expected an error when no API key is configured")
	}
}

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

func TestLenientUnmarshalCoercesNearMissShapes(t *testing.T) {
	var intent Intent
	if err := lenientUnmarshal([]byte(`{"usage":"family","city":["تهران"],"max_price_irr":"50000000000"}`), &intent); err != nil {
		t.Fatal(err)
	}
	if len(intent.Usage) != 1 || intent.Usage[0] != "family" || intent.City != "تهران" || intent.MaxPriceIRR != 50000000000 {
		t.Fatalf("unexpected intent %+v", intent)
	}
	var compare CompareResponse
	if err := lenientUnmarshal([]byte(`{"recommendations":[{"id":"7","fit":"خوب","best_for":"شهر","tradeoffs":"مصرف"}],"overall_recommendation":{"choice":"گزینه ۷"}}`), &compare); err != nil {
		t.Fatal(err)
	}
	if compare.OverallRecommendation != "گزینه ۷" || compare.Recommendations[0].ID != 7 || len(compare.Recommendations[0].Tradeoffs) != 1 {
		t.Fatalf("unexpected comparison %+v", compare)
	}
}
