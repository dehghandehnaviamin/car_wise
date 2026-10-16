package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Listing struct {
	ID              int64    `json:"id"`
	Brand           string   `json:"brand"`
	Model           string   `json:"model"`
	Trim            string   `json:"trim"`
	Year            int      `json:"year"`
	PriceIRR        int64    `json:"price_irr"`
	MileageKM       int      `json:"mileage_km"`
	Color           string   `json:"color"`
	City            string   `json:"city"`
	Transmission    string   `json:"transmission"`
	FuelConsumption float64  `json:"fuel_consumption_l100km"`
	BodyCondition   string   `json:"body_condition"`
	Features        []string `json:"features"`
	ImageURL        string   `json:"image_url"`
	Source          string   `json:"source"`
	SourceURL       string   `json:"source_url"`
	Description     string   `json:"description"`
	PublishedAt     string   `json:"published_at"`
	LastSeenAt      string   `json:"last_seen_at"`
	Score           float64  `json:"ranking_score"`
	Reasons         []string `json:"reasons,omitempty"`
	Cautions        []string `json:"cautions,omitempty"`
}

type KnowledgeChunk struct {
	VehicleModel string `json:"vehicle_model"`
	Topic        string `json:"topic"`
	Content      string `json:"content"`
}

type Intent struct {
	RawQuery           string   `json:"raw_query"`
	City               string   `json:"city"`
	MaxPriceIRR        int64    `json:"max_price_irr"`
	MinPriceIRR        int64    `json:"min_price_irr"`
	MinYear            int      `json:"min_year"`
	MaxYear            int      `json:"max_year"`
	MinMileageKM       int      `json:"min_mileage_km"`
	MaxMileageKM       int      `json:"max_mileage_km"`
	Models             []string `json:"models"`
	Colors             []string `json:"colors"`
	Origins            []string `json:"origins"`
	BodyConditions     []string `json:"body_conditions"`
	BodyTypes          []string `json:"body_types"`
	EquipmentLevels    []string `json:"equipment_levels"`
	Powertrains        []string `json:"powertrains"`
	Transmission       string   `json:"transmission"`
	Usage              []string `json:"usage"`
	Priorities         []string `json:"priorities"`
	HardConstraints    []string `json:"hard_constraints"`
	Conflicts          []string `json:"conflicts"`
	MissingInformation []string `json:"missing_information"`
}

type SearchResponse struct {
	Items     []Listing `json:"items"`
	Total     int       `json:"total"`
	Page      int       `json:"page"`
	PageSize  int       `json:"page_size"`
	Intent    *Intent   `json:"intent,omitempty"`
	Knowledge []string  `json:"knowledge,omitempty"`
}

type AssistantRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

type AssistantResponse struct {
	SessionID string         `json:"session_id"`
	Message   string         `json:"message"`
	Intent    Intent         `json:"intent"`
	Results   SearchResponse `json:"results"`
	Advice    string         `json:"advice"`
	Tradeoffs []string       `json:"tradeoffs,omitempty"`
	// IntentSource is "llm" when the model extracted the intent, "fallback" when heuristics did.
	IntentSource string `json:"intent_source"`
}

type CompareRecommendation struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Fit       string   `json:"fit"`
	Tradeoffs []string `json:"tradeoffs,omitempty"`
	BestFor   string   `json:"best_for"`
}

type CompareResponse struct {
	Items                 []Listing               `json:"items"`
	Recommendations       []CompareRecommendation `json:"recommendations"`
	OverallRecommendation string                  `json:"overall_recommendation"`
	// Source is "llm" when the model wrote the recommendations, "fallback" when deterministic rules did.
	Source string `json:"source"`
}

type App struct {
	db     *pgxpool.Pool
	logger *slog.Logger
	ai     *OpenAI
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	ctx := context.Background()
	var db *pgxpool.Pool
	var err error
	for attempt := 1; attempt <= 20; attempt++ {
		db, err = pgxpool.New(ctx, dsn)
		if err == nil {
			err = db.Ping(ctx)
		}
		if err == nil {
			break
		}
		if db != nil {
			db.Close()
		}
		logger.Warn("waiting for database", "attempt", attempt, "error", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		logger.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := migrate(ctx, db); err != nil {
		logger.Error("migration failed", "error", err)
		os.Exit(1)
	}
	if err := seed(ctx, db, env("SEED_FILE", "data/real_listings.json"), os.Getenv("KNOWLEDGE_FILE")); err != nil {
		logger.Error("seed failed", "error", err)
		os.Exit(1)
	}

	app := &App{db: db, logger: logger, ai: &OpenAI{
		Key:             os.Getenv("OPENAI_API_KEY"),
		Model:           env("OPENAI_MODEL", "gpt-4o-mini"),
		BaseURL:         env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		ReasoningEffort: os.Getenv("OPENAI_REASONING_EFFORT"),
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", app.health)
	mux.HandleFunc("GET /api/v1/listings", app.listings)
	mux.HandleFunc("POST /api/v1/assistant/search", app.assistantSearch)
	mux.HandleFunc("POST /api/v1/compare", app.compare)
	server := &http.Server{Addr: ":" + env("PORT", "8080"), Handler: securityHeaders(cors(mux)), ReadTimeout: 20 * time.Second, ReadHeaderTimeout: 10 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	logger.Info("api listening", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "error", err)
	}
}

func migrate(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector;
CREATE TABLE IF NOT EXISTS listings (
 id BIGSERIAL PRIMARY KEY, brand TEXT NOT NULL, model TEXT NOT NULL, trim TEXT NOT NULL DEFAULT '', year INT NOT NULL,
 price_irr BIGINT NOT NULL, mileage_km INT NOT NULL, color TEXT NOT NULL DEFAULT '', city TEXT NOT NULL,
 transmission TEXT NOT NULL DEFAULT '', fuel_consumption_l100km REAL NOT NULL DEFAULT 0, body_condition TEXT NOT NULL DEFAULT '',
 features JSONB NOT NULL DEFAULT '[]', image_url TEXT NOT NULL DEFAULT '', source TEXT NOT NULL, source_url TEXT NOT NULL,
 description TEXT NOT NULL DEFAULT '', published_at TIMESTAMPTZ, last_seen_at TIMESTAMPTZ, search_text TSVECTOR GENERATED ALWAYS AS
 (to_tsvector('simple', coalesce(brand,'') || ' ' || coalesce(model,'') || ' ' || coalesce(trim,'') || ' ' || coalesce(description,''))) STORED,
 UNIQUE(source, source_url));
CREATE INDEX IF NOT EXISTS listings_search_idx ON listings USING GIN(search_text);
CREATE TABLE IF NOT EXISTS knowledge_chunks (id BIGSERIAL PRIMARY KEY, vehicle_model TEXT NOT NULL, topic TEXT NOT NULL, content TEXT NOT NULL, embedding vector(1536));`)
	if err != nil {
		return err
	}
	// Remove records from the former demo fixture without touching source-linked snapshots.
	if _, err := db.Exec(ctx, `DELETE FROM listings WHERE source IN ('karnameh', 'khodro45') OR source_url IN ('https://divar.ir/', 'https://www.hamrah-mechanic.com/')`); err != nil {
		return err
	}
	// Knowledge chunks were previously synthetic. Keep the table empty until a vetted
	// real knowledge dataset is explicitly configured through KNOWLEDGE_FILE.
	if os.Getenv("KNOWLEDGE_FILE") == "" {
		if _, err := db.Exec(ctx, "DELETE FROM knowledge_chunks"); err != nil {
			return err
		}
	}
	return nil
}

func seed(ctx context.Context, db *pgxpool.Pool, listingFile, knowledgeFile string) error {
	if !fileExists(listingFile) {
		return fmt.Errorf("listing seed file not found: %s", listingFile)
	}
	b, err := os.ReadFile(listingFile)
	if err != nil {
		return err
	}
	var items []Listing
	if err := json.Unmarshal(b, &items); err != nil {
		return err
	}
	// Reconcile the database with the checked-in real snapshot on every startup.
	// This removes stale demo rows from an existing Docker volume and prevents
	// them from surviving a rebuild or being returned by the API later.
	allowedURLs := make([]string, 0, len(items))
	for _, l := range items {
		if strings.TrimSpace(l.SourceURL) != "" {
			allowedURLs = append(allowedURLs, l.SourceURL)
		}
	}
	if _, err := db.Exec(ctx, "DELETE FROM listings WHERE NOT (source_url = ANY($1::text[]))", allowedURLs); err != nil {
		return err
	}
	for _, l := range items {
		publishedAt := nullableTimestamp(l.PublishedAt)
		lastSeenAt := nullableTimestamp(l.LastSeenAt)
		_, err := db.Exec(ctx, `INSERT INTO listings (brand,model,trim,year,price_irr,mileage_km,color,city,transmission,fuel_consumption_l100km,body_condition,features,image_url,source,source_url,description,published_at,last_seen_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) ON CONFLICT DO NOTHING`, l.Brand, l.Model, l.Trim, l.Year, l.PriceIRR, l.MileageKM, l.Color, l.City, l.Transmission, l.FuelConsumption, l.BodyCondition, l.Features, l.ImageURL, l.Source, l.SourceURL, l.Description, publishedAt, lastSeenAt)
		if err != nil {
			return err
		}
	}
	var knowledgeCount int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM knowledge_chunks").Scan(&knowledgeCount); err != nil {
		return err
	}
	if knowledgeCount == 0 && knowledgeFile != "" && fileExists(knowledgeFile) {
		b, err := os.ReadFile(knowledgeFile)
		if err != nil {
			return err
		}
		var chunks []KnowledgeChunk
		if err := json.Unmarshal(b, &chunks); err != nil {
			return err
		}
		for _, k := range chunks {
			if _, err := db.Exec(ctx, "INSERT INTO knowledge_chunks (vehicle_model,topic,content) VALUES ($1,$2,$3)", k.VehicleModel, k.Topic, k.Content); err != nil {
				return err
			}
		}
	}
	return nil
}

func nullableTimestamp(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) serverError(w http.ResponseWriter, err error) {
	a.logger.Error("request failed", "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func (a *App) listings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := intParam(q.Get("page"), 1)
	if page > 100000 {
		page = 100000
	}
	size := intParam(q.Get("page_size"), 12)
	if size > 50 {
		size = 50
	}
	where, args := []string{"1=1"}, []any{}
	add := func(sql string, v any) { args = append(args, v); where = append(where, fmt.Sprintf(sql, len(args))) }
	if v := q.Get("city"); v != "" {
		add("city = $%d", v)
	}
	if v := q.Get("model"); v != "" {
		args = append(args, "%"+v+"%", "%"+v+"%")
		where = append(where, fmt.Sprintf("(model ILIKE $%d OR brand ILIKE $%d)", len(args)-1, len(args)))
	}
	if v := q.Get("max_price_irr"); v != "" {
		if n, e := strconv.ParseInt(v, 10, 64); e == nil {
			add("price_irr <= $%d::bigint", n)
		}
	}
	if v := q.Get("min_price_irr"); v != "" {
		if n, e := strconv.ParseInt(v, 10, 64); e == nil {
			add("price_irr >= $%d::bigint", n)
		}
	}
	if v := q.Get("query"); v != "" {
		if len([]rune(v)) > 160 {
			http.Error(w, "query is too long", http.StatusBadRequest)
			return
		}
		// Search every token across the user-visible listing fields. The generated
		// tsvector is useful for broad matching, but it intentionally omits color
		// and does not handle a phrase such as "سمند مشکی" reliably.
		tokens := searchTokens(v)
		groups := make([]string, 0, len(tokens))
		for _, token := range tokens {
			pattern := "%" + token + "%"
			args = append(args, pattern, pattern, pattern, pattern, pattern)
			start := len(args) - 4
			groups = append(groups, fmt.Sprintf("(brand ILIKE $%d OR model ILIKE $%d OR trim ILIKE $%d OR color ILIKE $%d OR description ILIKE $%d)", start, start+1, start+2, start+3, start+4))
		}
		if len(groups) > 1 {
			where = append(where, "("+strings.Join(groups, " AND ")+")")
		} else if len(groups) == 1 {
			where = append(where, groups[0])
		}
	}
	base := "SELECT id,brand,model,trim,year,price_irr,mileage_km,color,city,transmission,fuel_consumption_l100km,body_condition,features,image_url,source,source_url,description,coalesce(published_at::text,''),coalesce(last_seen_at::text,'') FROM listings WHERE " + strings.Join(where, " AND ")
	var total int
	if err := a.db.QueryRow(r.Context(), "SELECT count(*) FROM listings WHERE "+strings.Join(where, " AND "), args...).Scan(&total); err != nil {
		a.serverError(w, err)
		return
	}
	args = append(args, size, (page-1)*size)
	rows, err := a.db.Query(r.Context(), base+fmt.Sprintf(" ORDER BY last_seen_at DESC NULLS LAST, price_irr ASC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		a.serverError(w, err)
		return
	}
	defer rows.Close()
	items, err := scanListings(rows)
	if err != nil {
		a.serverError(w, err)
		return
	}
	jsonResponse(w, 200, SearchResponse{Items: items, Total: total, Page: page, PageSize: size})
}

func (a *App) assistantSearch(w http.ResponseWriter, r *http.Request) {
	var req AssistantRequest
	if err := decodeJSON(w, r, &req, 16<<10); err != nil || strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message is required", 400)
		return
	}
	started := time.Now()
	intentSource := "llm"
	intent, err := a.ai.ExtractIntent(r.Context(), req.Message)
	if err != nil {
		intentSource = "fallback"
		a.logger.Warn("intent extraction fallback", "error", err, "duration", time.Since(started))
		intent = heuristicIntent(req.Message)
	} else {
		a.logger.Info("intent extracted by llm", "model", a.ai.Model, "duration", time.Since(started))
	}
	intent.RawQuery = req.Message
	intent = enrichIntent(intent, req.Message)
	items, err := a.rank(r.Context(), intent)
	if err != nil {
		a.serverError(w, err)
		return
	}
	knowledge, _ := a.retrieveKnowledge(r.Context(), req.Message, intent.Models)
	if req.SessionID == "" {
		req.SessionID = fmt.Sprintf("local-%d", time.Now().UnixNano())
	}
	msg, tradeoffs := assistantSummary(intent, items)
	jsonResponse(w, 200, AssistantResponse{SessionID: req.SessionID, Message: msg, Advice: msg, Tradeoffs: tradeoffs, Intent: intent, IntentSource: intentSource, Results: SearchResponse{Items: items, Total: len(items), Page: 1, PageSize: len(items), Intent: &intent, Knowledge: knowledge}})
}

func (a *App) compare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := decodeJSON(w, r, &req, 8<<10); err != nil || len(req.IDs) == 0 {
		http.Error(w, "ids are required", 400)
		return
	}
	ids := uniquePositiveIDs(req.IDs)
	if len(ids) > 3 {
		http.Error(w, "compare supports at most 3 listings", http.StatusBadRequest)
		return
	}
	if len(ids) == 0 {
		http.Error(w, "ids are required", http.StatusBadRequest)
		return
	}
	rows, err := a.db.Query(r.Context(), "SELECT id,brand,model,trim,year,price_irr,mileage_km,color,city,transmission,fuel_consumption_l100km,body_condition,features,image_url,source,source_url,description,coalesce(published_at::text,''),coalesce(last_seen_at::text,'') FROM listings WHERE id = ANY($1)", ids)
	if err != nil {
		a.serverError(w, err)
		return
	}
	defer rows.Close()
	items, err := scanListings(rows)
	if err != nil {
		a.serverError(w, err)
		return
	}
	if len(items) != len(ids) {
		http.Error(w, "one or more listings were not found", http.StatusNotFound)
		return
	}
	started := time.Now()
	source := "llm"
	recommendations, overall, err := a.ai.ExplainComparison(r.Context(), items)
	if err != nil {
		source = "fallback"
		a.logger.Warn("comparison explanation fallback", "error", err, "duration", time.Since(started))
		recommendations, overall = deterministicCompare(items)
	} else {
		a.logger.Info("comparison explained by llm", "model", a.ai.Model, "duration", time.Since(started))
	}
	jsonResponse(w, 200, CompareResponse{Items: items, Recommendations: recommendations, OverallRecommendation: overall, Source: source})
}

func deterministicCompare(items []Listing) ([]CompareRecommendation, string) {
	recommendations := make([]CompareRecommendation, 0, len(items))
	bestID := int64(0)
	bestValue := -1.0
	for _, item := range items {
		value := 0.0
		if item.PriceIRR > 0 {
			value += 1 / float64(item.PriceIRR)
		}
		if item.MileageKM > 0 {
			value += 1 / float64(item.MileageKM+1)
		}
		if isOptionRich(item) {
			value += 0.00001
		}
		if value > bestValue {
			bestValue, bestID = value, item.ID
		}
		tradeoffs := make([]string, 0, 2)
		if item.MileageKM > 120000 {
			tradeoffs = append(tradeoffs, "کارکرد بالاتر است و کارشناسی سابقه سرویس اهمیت بیشتری دارد")
		}
		if item.BodyCondition == "" || item.BodyCondition == "کارشناسی‌شده" {
			tradeoffs = append(tradeoffs, "وضعیت بدنه در snapshot قطعی نیست")
		}
		fit := "گزینه‌ای متعادل برای مقایسه"
		bestFor := "خریدارانی که می‌خواهند چند آگهی واقعی را کنار هم بسنجند"
		if isCompact(item) {
			fit, bestFor = "جمع‌وجورتر و مناسب‌تر برای شهر", "رفت‌وآمد روزانه و پارک آسان"
		} else if vehicleClass(item) == "sedan" || vehicleClass(item) == "suv" {
			fit, bestFor = "کابین و سواری مناسب‌تر برای خانواده", "سفر و استفاده خانوادگی"
		}
		recommendations = append(recommendations, CompareRecommendation{ID: item.ID, Title: fmt.Sprintf("چرا %s %s؟", item.Brand, item.Model), Fit: fit, Tradeoffs: tradeoffs, BestFor: bestFor})
	}
	overall := "هر سه گزینه را با کارشناسی حضوری، وضعیت بدنه و سابقه سرویس نهایی کن."
	if bestID != 0 {
		for _, item := range items {
			if item.ID == bestID {
				overall = fmt.Sprintf("برای ارزش خرید اولیه، %s %s انتخاب متعادل‌تری به نظر می‌رسد؛ قبل از معامله کارشناسی را جدی بگیر.", item.Brand, item.Model)
				break
			}
		}
	}
	return recommendations, overall
}

func (a *App) rank(ctx context.Context, intent Intent) ([]Listing, error) {
	noPaint := false
	for _, constraint := range intent.HardConstraints {
		if constraint == "no_paint" || constraint == "بدون رنگ" {
			noPaint = true
		}
	}
	modelFilter := "%"
	if len(intent.Models) > 0 {
		modelFilter = modelSearchTerm(intent.Models[0])
	}
	rows, err := a.db.Query(ctx, "SELECT id,brand,model,trim,year,price_irr,mileage_km,color,city,transmission,fuel_consumption_l100km,body_condition,features,image_url,source,source_url,description,coalesce(published_at::text,''),coalesce(last_seen_at::text,'') FROM listings WHERE ($1='' OR city ILIKE $1) AND ($2::bigint=0 OR price_irr <= $2::bigint) AND ($3::bigint=0 OR price_irr >= $3::bigint) AND ($4::bigint=0 OR mileage_km <= $4::bigint) AND ($5::boolean=false OR body_condition ILIKE '%بدون رنگ%') AND ($6='%' OR (model ILIKE $6 OR brand ILIKE $6))", cityPattern(intent.City), intent.MaxPriceIRR, intent.MinPriceIRR, intent.MaxMileageKM, noPaint, modelFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanListings(rows)
	if err != nil {
		return nil, err
	}
	filtered := items[:0]
	for _, item := range items {
		if matchesIntent(item, intent) {
			filtered = append(filtered, item)
		}
	}
	items = filtered
	for i := range items {
		score, reasons, cautions := scoreListing(items[i], intent)
		items[i].Score = score
		items[i].Reasons = reasons
		items[i].Cautions = cautions
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].Score > items[i].Score {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	if len(items) > 12 {
		items = items[:12]
	}
	return items, nil
}

func modelSearchTerm(model string) string {
	model = strings.TrimSpace(model)
	known := map[string]string{
		"KMC J7": "J7", "آریزو 6 پرو": "X55", "پژو 207": "207", "پژو 206": "206",
		"X55 Pro": "X55", "C-HR": "CHR", "بی ام و سری 3": "3",
	}
	if value, ok := known[model]; ok {
		return "%" + value + "%"
	}
	return "%" + model + "%"
}

func searchTokens(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	seen := map[string]struct{}{}
	tokens := make([]string, 0, 4)
	for _, raw := range strings.Fields(query) {
		token := strings.Trim(raw, " ,،.؛:!?؟()[]{}\"'")
		if token == "" {
			continue
		}
		key := strings.ToLower(token)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tokens = append(tokens, token)
	}
	return tokens
}

func cityPattern(city string) string {
	if strings.TrimSpace(city) == "" {
		return "%"
	}
	return "%" + strings.TrimSpace(city) + "%"
}

func matchesIntent(item Listing, intent Intent) bool {
	if intent.MinYear > 0 && item.Year < intent.MinYear {
		return false
	}
	if intent.MaxYear > 0 && item.Year > intent.MaxYear {
		return false
	}
	if intent.MinMileageKM > 0 && item.MileageKM < intent.MinMileageKM {
		return false
	}
	if intent.MaxMileageKM > 0 && item.MileageKM > intent.MaxMileageKM {
		return false
	}
	if intent.Transmission != "" && !transmissionMatches(item, intent.Transmission) {
		return false
	}
	if len(intent.BodyConditions) > 0 {
		matched := false
		for _, wanted := range intent.BodyConditions {
			if bodyConditionMatches(item.BodyCondition, wanted) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(intent.Colors) > 0 {
		matched := false
		for _, wanted := range intent.Colors {
			if colorMatches(item.Color, wanted) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(intent.Origins) > 0 {
		matched := false
		for _, wanted := range intent.Origins {
			if originMatches(item, wanted) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if hasValue(intent.Priorities, "luxury") && !isLuxury(item) {
		return false
	}
	if hasValue(intent.Priorities, "non_luxury") && isLuxury(item) {
		return false
	}
	if hasValue(intent.Priorities, "non_economic") && item.PriceIRR <= 50000000000 {
		return false
	}
	if hasValue(intent.Priorities, "no_options") && isOptionRich(item) {
		return false
	}
	if hasValue(intent.Priorities, "compact") && !isCompact(item) {
		return false
	}
	if len(intent.BodyTypes) > 0 {
		matched := false
		for _, wanted := range intent.BodyTypes {
			if vehicleClass(item) == wanted {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if containsAny(intent.Powertrains, "hybrid", "electric") && !isHybridOrElectric(item) {
		return false
	}
	if len(intent.Conflicts) > 0 {
		return false
	}
	return true
}

func transmissionMatches(item Listing, wanted string) bool {
	wanted = strings.ToLower(wanted)
	automatic := containsAny([]string{item.Transmission}, "اتومات", "automatic", "auto")
	if wanted == "automatic" {
		return automatic
	}
	if wanted == "manual" {
		return !automatic && item.Transmission != ""
	}
	return strings.Contains(strings.ToLower(item.Transmission), wanted)
}

func bodyConditionMatches(actual, wanted string) bool {
	actual = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(actual, "‌", ""), " ", ""))
	switch wanted {
	case "no_paint":
		return strings.Contains(actual, "بدونرنگ")
	case "painted":
		return strings.Contains(actual, "رنگ") && !strings.Contains(actual, "بدونرنگ")
	case "repaired":
		return strings.Contains(actual, "صافکاری") || strings.Contains(actual, "نیم") || strings.Contains(actual, "scratch")
	case "accident":
		return strings.Contains(actual, "تصادف") || strings.Contains(actual, "accident") || strings.Contains(actual, "شاسی")
	default:
		return strings.Contains(actual, strings.ToLower(wanted))
	}
}

func colorMatches(actual, wanted string) bool {
	actual = strings.ToLower(strings.TrimSpace(actual))
	wanted = strings.ToLower(strings.TrimSpace(wanted))
	aliases := map[string][]string{
		"مشکی": {"مشکی", "سیاه", "black"}, "سیاه": {"مشکی", "سیاه", "black"},
		"سفید": {"سفید", "white"}, "خاکستری": {"خاکستری", "طوسی", "grey", "gray"},
		"نقره ای": {"نقره", "نقره ای", "silver"},
	}
	if values, ok := aliases[wanted]; ok {
		return containsAny([]string{actual}, values...)
	}
	return strings.Contains(actual, wanted)
}

func originMatches(item Listing, wanted string) bool {
	wanted = strings.ToLower(strings.TrimSpace(wanted))
	brand := strings.ToLower(item.Brand)
	chinese := containsAny([]string{brand}, "چری", "mvm", "هایما", "kmc", "jac", "byd", "mg", "ام جی")
	iranian := containsAny([]string{brand}, "ایران خودرو", "سایپا")
	korean := containsAny([]string{brand}, "هیوندای", "کیا")
	japanese := containsAny([]string{brand}, "تویوتا", "نیسان")
	german := containsAny([]string{brand}, "بنز", "بی ام و", "bmw", "مرسدس")
	switch wanted {
	case "chinese", "چینی":
		return chinese
	case "iranian", "ایرانی":
		return iranian
	case "korean", "کره ای", "کره‌ای":
		return korean
	case "japanese", "ژاپنی":
		return japanese
	case "german", "آلمانی":
		return german
	case "foreign", "خارجی":
		return !iranian && (chinese || korean || japanese || german || brand != "")
	default:
		return strings.Contains(brand, wanted)
	}
}

func isLuxury(item Listing) bool {
	return containsAny([]string{item.Brand}, "بنز", "بی ام و", "bmw", "مرسدس") ||
		containsAny([]string{item.Model, item.Trim}, "a کلاس", "c کلاس", "سری 3", "سری 5", "x1", "x3", "x5", "gt", "premium", "luxury") ||
		containsAny(item.Features, "luxury", "premium")
}

func isCompact(item Listing) bool {
	return containsAny([]string{item.Brand, item.Model, item.Trim}, "پراید", "کوییک", "ساینا", "پژو 206", "پژو 207", "206", "207", "ریو", "j4", "j5", "i10", "a کلاس", "yaris", "c-hr", "chr")
}

func vehicleClass(item Listing) string {
	text := strings.ToLower(strings.Join([]string{item.Model, item.Trim}, " "))
	switch {
	case containsAny([]string{text}, "پیکاپ", "pickup", "وانت"):
		return "pickup"
	case containsAny([]string{text}, "هاچ", "hatch", "پراید", "کوییک", "ساینا", "206", "207", "i10", "ریو", "mg 3", "110", "315"):
		return "hatchback"
	case containsAny([]string{text}, "کوپه", "coupe", "gt"):
		return "coupe"
	case containsAny([]string{text}, "suv", "اس یو وی", "شاسی", "x1", "x2", "x3", "x5", "s3", "s5", "s6", "s7", "s8", "k7", "x5", "x22", "x33", "x55", "7x", "c-hr", "rav4", "توسان", "سانتافه", "اسپورتیج", "سورنتو", "جوک", "قشقایی", "ترا", "sealion", "song"):
		return "suv"
	case containsAny([]string{text}, "سدان", "sedan", "دنا", "سمند", "کرولا", "سوناتا", "النترا", "اکسنت", "اپتیما", "سراتو", "آلتیما", "تیانا", "سری 3", "سری 5", "a کلاس", "c کلاس", "j4", "j5", "j7", "a5", "eagle", "550", "6", "360"):
		return "sedan"
	default:
		return "other"
	}
}

func isHybridOrElectric(item Listing) bool {
	return containsAny([]string{item.Brand, item.Model, item.Trim}, "byd", "qin", "destroyer", "sealion", "hybrid", "برقی", "هیبرید") ||
		containsAny(item.Features, "hybrid", "electric", "برقی", "هیبرید")
}

func isEfficient(item Listing) bool {
	return isHybridOrElectric(item) || containsAny([]string{item.Brand, item.Model, item.Trim}, "پراید", "کوییک", "ساینا", "206", "207", "i10", "ریو", "j4", "corolla", "کرولا")
}

func isHighConsumption(item Listing) bool {
	return containsAny([]string{item.Model, item.Trim}, "x5", "x3", "سری 5", "سورنتو", "سانتافه", "آلتیما", "ترا", "v6", "v8", "6 سیلندر", "8 سیلندر")
}

func isOptionRich(item Listing) bool {
	return containsAny([]string{item.Model, item.Trim}, "آپشن", "اپشن", "فول", "premium", "luxury", "پلاس") || len(item.Features) >= 5
}

func scoreListing(item Listing, intent Intent) (float64, []string, []string) {
	score := 0.0
	reasons := make([]string, 0, 5)
	cautions := make([]string, 0, 3)
	if intent.MaxPriceIRR > 0 {
		budgetRatio := float64(item.PriceIRR) / float64(intent.MaxPriceIRR)
		switch {
		case budgetRatio <= 0.80:
			score += 25
			reasons = append(reasons, "حاشیه امن نسبت به سقف بودجه")
		case budgetRatio <= 1:
			score += 20
			reasons = append(reasons, "داخل سقف بودجه")
		}
	} else {
		score += 10
		reasons = append(reasons, "قیمت برای مقایسه در دسترس است")
	}
	if intent.MinPriceIRR > 0 && item.PriceIRR >= intent.MinPriceIRR {
		score += 5
	}
	if item.MileageKM > 0 {
		switch {
		case item.MileageKM <= 50000:
			score += 20
			reasons = append(reasons, "کارکرد پایین")
		case item.MileageKM <= 120000:
			score += 12
			reasons = append(reasons, "کارکرد متعادل")
		default:
			cautions = append(cautions, "کارکرد بالا؛ کارشناسی و سابقه سرویس مهم است")
		}
	} else {
		cautions = append(cautions, "کارکرد در آگهی مشخص نیست")
	}
	if item.Year >= 1400 || (item.Year >= 2012 && item.Year <= 2026) {
		score += 8
	}
	if strings.Contains(strings.ToLower(item.Transmission), "اتومات") || strings.Contains(strings.ToLower(item.Transmission), "automatic") {
		score += 5
		reasons = append(reasons, "گیربکس اتوماتیک")
	}
	if strings.Contains(item.BodyCondition, "بدون رنگ") {
		score += 12
		reasons = append(reasons, "بدون رنگ طبق داده منبع")
	} else if item.BodyCondition == "کارشناسی‌شده" || item.BodyCondition == "" {
		cautions = append(cautions, "وضعیت رنگ از داده منبع قطعی نیست")
	}
	for _, priority := range intent.Priorities {
		switch priority {
		case "low_fuel":
			if (item.FuelConsumption > 0 && item.FuelConsumption <= 7.5) || isEfficient(item) {
				score += 18
				reasons = append(reasons, "مصرف سوخت مناسب شهر")
			} else {
				cautions = append(cautions, "مصرف سوخت در snapshot منبع ثبت نشده")
			}
		case "high_fuel":
			if isHighConsumption(item) {
				score += 10
				reasons = append(reasons, "توان و مصرف بالاتر برای مسیرهای طولانی")
			}
		case "space":
			if vehicleClass(item) == "sedan" || vehicleClass(item) == "suv" || containsAny(item.Features, "spacious", "family", "comfort") || containsAny([]string{item.Model, item.Trim}, "سانتافه", "توسان", "کمری", "سوناتا", "s7", "s8", "x55", "x5", "sorento") {
				score += 15
				reasons = append(reasons, "تناسب بهتر با خانواده و سفر")
			}
		case "luxury":
			if containsAny(item.Features, "luxury", "premium") || containsAny([]string{item.Brand, item.Model}, "بنز", "بی ام و", "bmw", "مرسدس", "فونیکس") {
				score += 15
				reasons = append(reasons, "طراحی یا جایگاه لوکس‌تر")
			}
		case "options":
			if isOptionRich(item) {
				score += 15
				reasons = append(reasons, "امکانات ثبت‌شده بیشتر")
			}
		case "easy_parking":
			if item.MileageKM <= 100000 || containsAny(item.Features, "easy parking", "city") {
				score += 10
				reasons = append(reasons, "مناسب‌تر برای استفاده شهری")
			}
		case "compact":
			if isCompact(item) {
				score += 22
				reasons = append(reasons, "ابعاد مناسب‌تر برای شهر و پارک آسان")
			}
		case "comfort":
			if vehicleClass(item) == "sedan" || vehicleClass(item) == "suv" {
				score += 16
				reasons = append(reasons, "کابین و سواری مناسب‌تر برای راحتی و سفر")
			}
		case "city_automatic":
			if transmissionMatches(item, "automatic") {
				score += 12
				reasons = append(reasons, "اتوماتیک مناسب‌تر برای رانندگی شهری")
			}
		case "economic":
			if item.PriceIRR <= 50000000000 {
				score += 10
				reasons = append(reasons, "در محدوده اقتصادی تعریف‌شده")
			}
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "مطابقت پایه با فیلترهای درخواست")
	}
	return score, reasons, uniqueStrings(cautions)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func (a *App) retrieveKnowledge(ctx context.Context, query string, models []string) ([]string, error) {
	term := strings.TrimSpace(query)
	if len([]rune(term)) > 12 {
		term = string([]rune(term)[:12])
	}
	rows, err := a.db.Query(ctx, "SELECT content FROM knowledge_chunks WHERE content ILIKE $1 OR vehicle_model ILIKE $2 LIMIT 4", "%"+term+"%", "%"+strings.Join(models, " ")+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err == nil {
			out = append(out, s)
		}
	}
	return out, nil
}

func scanListings(rows pgx.Rows) ([]Listing, error) {
	out := make([]Listing, 0)
	for rows.Next() {
		var l Listing
		if err := rows.Scan(&l.ID, &l.Brand, &l.Model, &l.Trim, &l.Year, &l.PriceIRR, &l.MileageKM, &l.Color, &l.City, &l.Transmission, &l.FuelConsumption, &l.BodyCondition, &l.Features, &l.ImageURL, &l.Source, &l.SourceURL, &l.Description, &l.PublishedAt, &l.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func heuristicIntent(s string) Intent {
	s = strings.ToLower(normalizeDigits(s))
	s = strings.ReplaceAll(s, "‌", "")
	i := Intent{City: "تهران"}
	if strings.Contains(s, "کم مصرف") || strings.Contains(s, "کممصرف") || strings.Contains(s, "بنزین") {
		i.Priorities = append(i.Priorities, "low_fuel")
	}
	if strings.Contains(s, "جادار") || strings.Contains(s, "خانواده") || strings.Contains(s, "خانواد") {
		i.Priorities = append(i.Priorities, "space")
	}
	if strings.Contains(s, "غیر لوکس") || strings.Contains(s, "غیرلوکس") {
		i.Priorities = append(i.Priorities, "non_luxury")
	} else if strings.Contains(s, "لوکس") || strings.Contains(s, "لاکچری") {
		i.Priorities = append(i.Priorities, "luxury")
	}
	if strings.Contains(s, "غیر اقتصادی") || strings.Contains(s, "غیراقتصادی") {
		i.Priorities = append(i.Priorities, "non_economic")
	} else if strings.Contains(s, "اقتصادی") || strings.Contains(s, "به صرفه") || strings.Contains(s, "بصرفه") {
		i.Priorities = append(i.Priorities, "economic")
		if i.MaxPriceIRR == 0 {
			i.MaxPriceIRR = 50000000000
		}
	}
	if strings.Contains(s, "بدون آپشن") || strings.Contains(s, "بدونآپشن") {
		i.Priorities = append(i.Priorities, "no_options")
	} else if strings.Contains(s, "آپشن") {
		i.Priorities = append(i.Priorities, "options")
		i.EquipmentLevels = append(i.EquipmentLevels, "optional")
	}
	if strings.Contains(s, "فول") || strings.Contains(s, "فول آپشن") || strings.Contains(s, "فول‌آپشن") {
		i.Priorities = append(i.Priorities, "options")
		i.EquipmentLevels = append(i.EquipmentLevels, "full")
	}
	if strings.Contains(s, "اکسلنت") || strings.Contains(s, "excellent") {
		i.EquipmentLevels = append(i.EquipmentLevels, "excellent")
	}
	if strings.Contains(s, "نرم") || strings.Contains(s, "راحت") || strings.Contains(s, "سواری") {
		i.Priorities = append(i.Priorities, "comfort")
	}
	if strings.Contains(s, "پارک") || strings.Contains(s, "جمع و جور") || strings.Contains(s, "جمعوجور") || strings.Contains(s, "کوچک") {
		i.Priorities = append(i.Priorities, "easy_parking")
		i.Priorities = append(i.Priorities, "compact")
	}
	if strings.Contains(s, "داخل شهر") || strings.Contains(s, "درون شهری") || strings.Contains(s, "شهری") {
		i.Usage = append(i.Usage, "city")
		i.Priorities = append(i.Priorities, "city_automatic")
	}
	if strings.Contains(s, "جاده") || strings.Contains(s, "بین شهری") || strings.Contains(s, "مسافرت") {
		i.Usage = append(i.Usage, "highway")
		i.Priorities = append(i.Priorities, "comfort")
	}
	if strings.Contains(s, "آفرود") || strings.Contains(s, "کمپ") || strings.Contains(s, "گردش") {
		i.Usage = append(i.Usage, "offroad")
		i.BodyTypes = append(i.BodyTypes, "suv", "pickup")
	}
	for phrase, class := range map[string]string{
		"هاچ بک": "hatchback", "هاچبک": "hatchback", "hatchback": "hatchback",
		"کوپه": "coupe", "coupe": "coupe", "سدان": "sedan", "sedan": "sedan",
		"اس یو وی": "suv", "suv": "suv", "شاسی بلند": "suv", "شاسی": "suv",
		"کراس اوور": "suv", "crossover": "suv", "پیکاپ": "pickup", "pickup": "pickup",
	} {
		if strings.Contains(s, phrase) {
			i.BodyTypes = append(i.BodyTypes, class)
		}
	}
	if strings.Contains(s, "اتومات") || strings.Contains(s, "automatic") {
		i.Transmission = "automatic"
	}
	if strings.Contains(s, "دنده ای") || strings.Contains(s, "دنده‌ای") || strings.Contains(s, "دستی") || strings.Contains(s, "manual") {
		i.Transmission = "manual"
	}
	if strings.Contains(s, "هیبرید") || strings.Contains(s, "برقی") || strings.Contains(s, "الکتریکی") {
		i.Powertrains = append(i.Powertrains, "hybrid", "electric")
		i.Priorities = append(i.Priorities, "low_fuel")
	}
	if strings.Contains(s, "4 سیلندر") {
		i.Priorities = append(i.Priorities, "low_fuel")
	}
	if strings.Contains(s, "پر مصرف") || strings.Contains(s, "پرمصرف") || strings.Contains(s, "6 سیلندر") || strings.Contains(s, "8 سیلندر") {
		i.Priorities = append(i.Priorities, "high_fuel")
	}
	for _, color := range []string{"مشکی", "سیاه", "سفید", "خاکستری", "طوسی", "نقره ای", "نقره‌ای", "قرمز", "آبی", "سبز", "قهوه ای", "قهوه‌ای"} {
		if strings.Contains(s, color) {
			i.Colors = append(i.Colors, color)
		}
	}
	switch {
	case strings.Contains(s, "چینی"):
		i.Origins = append(i.Origins, "chinese")
	case strings.Contains(s, "ایرانی"):
		i.Origins = append(i.Origins, "iranian")
	case strings.Contains(s, "کره ای") || strings.Contains(s, "کره‌ای"):
		i.Origins = append(i.Origins, "korean")
	case strings.Contains(s, "ژاپنی"):
		i.Origins = append(i.Origins, "japanese")
	case strings.Contains(s, "آلمانی"):
		i.Origins = append(i.Origins, "german")
	case strings.Contains(s, "خارجی"):
		i.Origins = append(i.Origins, "foreign")
	}
	if strings.Contains(s, "بدون رنگ") || strings.Contains(s, "بی رنگ") || strings.Contains(s, "بیرنگ") {
		i.HardConstraints = append(i.HardConstraints, "no_paint")
		i.BodyConditions = append(i.BodyConditions, "no_paint")
	}
	if strings.Contains(s, "رنگ دار") || strings.Contains(s, "رنگدار") || strings.Contains(s, "رنگ شده") || strings.Contains(s, "رنگ‌شده") {
		i.BodyConditions = append(i.BodyConditions, "painted")
	}
	if strings.Contains(s, "صافکاری") || strings.Contains(s, "نیم رنگ") || strings.Contains(s, "نیم‌رنگ") {
		i.BodyConditions = append(i.BodyConditions, "repaired")
	}
	if strings.Contains(s, "تصادفی") || strings.Contains(s, "تصادف") || strings.Contains(s, "شاسی خورده") {
		i.BodyConditions = append(i.BodyConditions, "accident")
	}
	i.MinYear, i.MaxYear = parseYearRange(s)
	i.MinMileageKM, i.MaxMileageKM = parseMileageRange(s)
	if min, max := parsePriceRange(s); min > 0 || max > 0 {
		i.MinPriceIRR, i.MaxPriceIRR = min, max
	}
	if hasValue(i.Priorities, "economic") && (i.MaxPriceIRR == 0 || i.MaxPriceIRR > 50000000000) {
		i.MaxPriceIRR = 50000000000
	}
	if hasValue(i.Priorities, "luxury") && i.MinPriceIRR == 0 {
		i.MinPriceIRR = 90000000000
	}
	if hasValue(i.Priorities, "economic") && (hasValue(i.Priorities, "luxury") || hasValue(i.Priorities, "options")) {
		i.Conflicts = append(i.Conflicts, "economic_conflicts_with_luxury_or_options")
	}
	if hasValue(i.Priorities, "economic") && i.MinPriceIRR > 50000000000 {
		i.Conflicts = append(i.Conflicts, "economic_budget_above_threshold")
	}
	if hasValue(i.Priorities, "luxury") && i.MaxPriceIRR > 0 && i.MaxPriceIRR < 90000000000 {
		i.Conflicts = append(i.Conflicts, "luxury_budget_below_threshold")
	}
	if hasValue(i.BodyTypes, "hatchback") && hasValue(i.Priorities, "space") {
		i.Conflicts = append(i.Conflicts, "hatchback_conflicts_with_family_space")
	}
	modelAliases := map[string][]string{
		"آریزو 6 پرو": {"آریزو 6", "آریزو ۶", "arrizo 6"}, "X55 Pro": {"x55", "x 55", "ایکس 55"},
		"KMC J7": {"kmc j7", "j7"}, "کمری": {"کمری", "camry"}, "سوناتا": {"سوناتا", "sonata"},
		"توسان": {"توسان", "tucson"}, "سراتو": {"سراتو", "cerato"}, "دنا": {"دنا"},
		"سمند": {"سمند", "samand"}, "پژو پارس": {"پژو پارس", "پارس", "405"},
		"پژو 207": {"پژو 207", "پژو ۲۰۷", "207", "۲۰۷"}, "پژو 206": {"پژو 206", "پژو ۲۰۶", "206", "۲۰۶"},
		"پراید": {"پراید", "pride"}, "شاهین": {"شاهین"}, "کوییک": {"کوییک", "quick"},
	}
	for model, aliases := range modelAliases {
		for _, alias := range aliases {
			if strings.Contains(s, strings.ToLower(alias)) {
				i.Models = append(i.Models, model)
				break
			}
		}
	}
	return i
}

func enrichIntent(intent Intent, message string) Intent {
	heuristic := heuristicIntent(message)
	if intent.City == "" {
		intent.City = heuristic.City
	}
	if heuristic.MinPriceIRR > 0 || heuristic.MaxPriceIRR > 0 {
		intent.MinPriceIRR = heuristic.MinPriceIRR
		intent.MaxPriceIRR = heuristic.MaxPriceIRR
	}
	if len(heuristic.Models) > 0 {
		intent.Models = heuristic.Models
	} else {
		intent.Models = mentionedModels(message, intent.Models)
	}
	if len(heuristic.Colors) > 0 {
		intent.Colors = heuristic.Colors
	}
	if len(heuristic.Origins) > 0 {
		intent.Origins = heuristic.Origins
	}
	if len(heuristic.BodyConditions) > 0 {
		intent.BodyConditions = heuristic.BodyConditions
	}
	if len(heuristic.BodyTypes) > 0 {
		intent.BodyTypes = heuristic.BodyTypes
	}
	if len(heuristic.EquipmentLevels) > 0 {
		intent.EquipmentLevels = heuristic.EquipmentLevels
	}
	if len(heuristic.Powertrains) > 0 {
		intent.Powertrains = heuristic.Powertrains
	}
	if heuristic.Transmission != "" {
		intent.Transmission = heuristic.Transmission
	}
	if heuristic.MinYear > 0 || heuristic.MaxYear > 0 {
		intent.MinYear, intent.MaxYear = heuristic.MinYear, heuristic.MaxYear
	}
	if heuristic.MinMileageKM > 0 || heuristic.MaxMileageKM > 0 {
		intent.MinMileageKM, intent.MaxMileageKM = heuristic.MinMileageKM, heuristic.MaxMileageKM
	}
	intent.Usage = appendUnique(intent.Usage, heuristic.Usage...)
	intent.Priorities = appendUnique(intent.Priorities, heuristic.Priorities...)
	intent.HardConstraints = appendUnique(intent.HardConstraints, heuristic.HardConstraints...)
	intent.Conflicts = appendUnique(intent.Conflicts, heuristic.Conflicts...)
	if intent.Models == nil {
		intent.Models = []string{}
	}
	if intent.Colors == nil {
		intent.Colors = []string{}
	}
	if intent.Origins == nil {
		intent.Origins = []string{}
	}
	if intent.BodyConditions == nil {
		intent.BodyConditions = []string{}
	}
	if intent.BodyTypes == nil {
		intent.BodyTypes = []string{}
	}
	if intent.EquipmentLevels == nil {
		intent.EquipmentLevels = []string{}
	}
	if intent.Powertrains == nil {
		intent.Powertrains = []string{}
	}
	if intent.Usage == nil {
		intent.Usage = []string{}
	}
	if intent.Priorities == nil {
		intent.Priorities = []string{}
	}
	if intent.HardConstraints == nil {
		intent.HardConstraints = []string{}
	}
	if intent.MissingInformation == nil {
		intent.MissingInformation = []string{}
	}
	if intent.Conflicts == nil {
		intent.Conflicts = []string{}
	}
	return intent
}

func mentionedModels(message string, models []string) []string {
	message = strings.ToLower(strings.ReplaceAll(normalizeDigits(message), "‌", ""))
	mentioned := make([]string, 0, len(models))
	for _, model := range models {
		candidate := strings.ToLower(strings.ReplaceAll(model, "‌", ""))
		if candidate != "" && strings.Contains(message, candidate) {
			mentioned = append(mentioned, model)
		}
	}
	return mentioned
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	unique := make([]string, 0, len(values)+len(additions))
	for _, value := range values {
		if value != "" && !seen[value] {
			unique = append(unique, value)
			seen[value] = true
		}
	}
	for _, value := range additions {
		if value != "" && !seen[value] {
			unique = append(unique, value)
			seen[value] = true
		}
	}
	return unique
}
func parseBillions(s string) int64 {
	values := priceNumbers(s)
	if len(values) > 0 {
		return values[0]
	}
	return 0
}
func priceNumbers(s string) []int64 {
	s = normalizeDigits(s)
	values := make([]int64, 0, 2)
	for _, token := range strings.Fields(s) {
		token = strings.Trim(token, "،,.؛:()[]")
		if n, err := strconv.ParseInt(token, 10, 64); err == nil && n > 0 && n < 100 {
			values = append(values, n)
		}
	}
	return values
}

func parseYearRange(s string) (int, int) {
	values := make([]int, 0, 2)
	for _, raw := range regexp.MustCompile(`\d+`).FindAllString(s, -1) {
		n, err := strconv.Atoi(raw)
		if err == nil && ((n >= 1300 && n <= 1500) || (n >= 2000 && n <= 2035)) {
			values = append(values, n)
		}
	}
	if len(values) >= 2 {
		return values[0], values[1]
	}
	if len(values) == 1 {
		if strings.Contains(s, "از") {
			return values[0], 0
		}
		if strings.Contains(s, "تا") || strings.Contains(s, "قبل") {
			return 0, values[0]
		}
		return values[0], values[0]
	}
	return 0, 0
}

func parseMileageRange(s string) (int, int) {
	if !strings.Contains(s, "کارکرد") && !strings.Contains(s, "کیلومتر") && !strings.Contains(s, "هزار") && !strings.Contains(strings.ToLower(s), "km") {
		return 0, 0
	}
	re := regexp.MustCompile(`(?i)(\d+)\s*(هزار|کیلومتر|km|k)?`)
	rawValues := make([]int, 0, 2)
	scaleThousands := false
	for _, match := range re.FindAllStringSubmatch(s, -1) {
		n, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		if n >= 1300 && n <= 1500 {
			continue
		}
		rawValues = append(rawValues, n)
		if match[2] == "هزار" || strings.EqualFold(match[2], "k") {
			scaleThousands = true
		}
	}
	if scaleThousands {
		for i, value := range rawValues {
			if value < 1000 {
				rawValues[i] = value * 1000
			}
		}
	}
	if len(rawValues) >= 2 && (strings.Contains(s, "بین") || strings.Contains(s, " تا ")) {
		return rawValues[0], rawValues[1]
	}
	if len(rawValues) == 0 {
		return 0, 0
	}
	if strings.Contains(s, "بیشتر") || strings.Contains(s, "بالا") {
		return rawValues[0], 0
	}
	return 0, rawValues[0]
}

func parsePriceRange(s string) (int64, int64) {
	values := priceNumbers(s)
	if len(values) == 0 {
		return 0, 0
	}
	if (strings.Contains(s, "بین") || strings.Contains(s, " تا ")) && len(values) >= 2 {
		return values[0] * 10000000000, values[1] * 10000000000
	}
	if strings.Contains(s, "تا") {
		return 0, values[0] * 10000000000
	}
	return 0, 0
}
func normalizeDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '۰', '٠':
			b.WriteByte('0')
		case '۱', '١':
			b.WriteByte('1')
		case '۲', '٢':
			b.WriteByte('2')
		case '۳', '٣':
			b.WriteByte('3')
		case '۴', '٤':
			b.WriteByte('4')
		case '۵', '٥':
			b.WriteByte('5')
		case '۶', '٦':
			b.WriteByte('6')
		case '۷', '٧':
			b.WriteByte('7')
		case '۸', '٨':
			b.WriteByte('8')
		case '۹', '٩':
			b.WriteByte('9')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
func assistantSummary(i Intent, items []Listing) (string, []string) {
	if len(items) == 0 {
		if containsAny(i.Conflicts, "luxury_budget_below_threshold") {
			return "با این بودجه، گزینه‌ای که واقعاً لوکس محسوب شود در داده فعلی وجود ندارد.", []string{"برای لوکس‌بودن سقف بودجه را به حدود ۹ میلیارد تومان یا بیشتر برسان", "یا معیار لوکس‌بودن را به طراحی و آپشن‌های بهتر تغییر بده"}
		}
		if containsAny(i.Conflicts, "economic_budget_above_threshold") {
			return "این بازه بالاتر از تعریف اقتصادی است؛ گزینه‌ها را اقتصادی رتبه‌بندی نکردم تا پیشنهاد گمراه‌کننده نباشد.", []string{"برای اقتصادی‌بودن سقف را زیر ۵ میلیارد تومان نگه دار", "یا معیار اقتصادی‌بودن را حذف کن"}
		}
		if len(i.Conflicts) > 0 {
			return "این ترکیب معیارها در بازار خودرو منطقی نیست؛ مثلاً اقتصادیِ لوکس یا هاچ‌بکِ خانوادگی هم‌زمان پیدا نمی‌شود.", []string{"یکی از معیارهای متضاد را حذف کن", "برای لوکس‌بودن بودجه را بالا ببر یا برای اقتصادی‌بودن آپشن و کلاس را منعطف کن"}
		}
		if hasValue(i.Priorities, "luxury") {
			return "در این بازه قیمت، در snapshot فعلی گزینه‌ای که واقعاً لوکس محسوب شود پیدا نشد؛ خودروهای اقتصادی را به‌عنوان جایگزین لوکس پیشنهاد نمی‌کنم.", []string{"برای بنز، بی‌ام‌و یا کلاس‌های لوکس‌تر، سقف بودجه را حدود ۴ میلیارد تومان یا بیشتر کن", "یا معیار لوکس‌بودن را به آپشن و طراحی بهترِ خودروهای اقتصادی تغییر بده"}
		}
		if len(i.Origins) > 0 {
			return "با این ملیت و بازه قیمت، آگهی منطبق و قابل اتکایی در داده فعلی پیدا نشد.", []string{"بازه قیمت را کمی بازتر کن", "یا ملیت‌های دیگر را برای مقایسه اضافه کن"}
		}
		return "با معیارهای فعلی آگهی دقیقی پیدا نشد. اگر یکی از معیارهای بودجه، کارکرد یا وضعیت بدنه را کمی منعطف کنی، گزینه‌های بیشتری بررسی می‌کنم.", []string{"افزایش سقف بودجه یا پذیرش مدل‌های بیشتر", "حذف شرط بدون رنگ", "پذیرش کارکرد بالاتر"}
	}
	best := items[0]
	message := fmt.Sprintf("بر اساس معیارهایت، %s %s بالاترین امتیاز تناسب را گرفت. دلیل اصلی: %s.", best.Brand, best.Model, strings.Join(best.Reasons[:minInt(2, len(best.Reasons))], " و "))
	tradeoffs := []string{}
	if len(best.Cautions) > 0 {
		tradeoffs = append(tradeoffs, "نکته قابل بررسی: "+strings.Join(best.Cautions, "؛ "))
	}
	if len(items) > 1 {
		tradeoffs = append(tradeoffs, fmt.Sprintf("گزینه‌های بعدی برای مقایسه: %s %s و %s %s.", items[1].Brand, items[1].Model, items[minInt(2, len(items)-1)].Brand, items[minInt(2, len(items)-1)].Model))
	}
	return message, tradeoffs
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func containsAny(xs []string, values ...string) bool {
	for _, x := range xs {
		for _, v := range values {
			if strings.Contains(strings.ToLower(x), v) {
				return true
			}
		}
	}
	return false
}

func hasValue(xs []string, value string) bool {
	for _, item := range xs {
		if item == value {
			return true
		}
	}
	return false
}

func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("request body must contain one JSON value")
	}
	return nil
}

func uniquePositiveIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}
func intParam(s string, d int) int {
	if n, e := strconv.Atoi(s); e == nil && n > 0 {
		return n
	}
	return d
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := env("CORS_ORIGIN", "http://localhost:5173")
		if requestOrigin := r.Header.Get("Origin"); requestOrigin == "" || requestOrigin == origin {
			if requestOrigin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
		} else if requestOrigin != "" {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

type OpenAI struct {
	Key     string
	Model   string
	BaseURL string
	// ReasoningEffort is sent as reasoning.effort when set; leave empty for models without reasoning support.
	ReasoningEffort string
}

const intentInstructions = "Extract Persian used-car search intent. Return JSON keys: city, min_price_irr, max_price_irr, min_year, max_year, min_mileage_km, max_mileage_km, models, colors, origins, body_conditions, body_types, equipment_levels, powertrains, transmission, usage, priorities, hard_constraints, conflicts, missing_information. Use integer IRR; if user says billions in Iranian car context, interpret as toman and multiply by 10. origins may contain only chinese, iranian, foreign, korean, japanese, german. body_types may contain sedan, hatchback, coupe, suv, pickup. body_conditions may contain no_paint, painted, repaired, accident. powertrains may contain hybrid, electric, combustion. priorities may contain luxury, non_luxury, economic, non_economic, options, no_options, comfort, low_fuel, high_fuel, compact. Infer relations conservatively: economic means <= 5 billion toman, luxury means >= 9 billion toman; small usually hatchback/coupe; comfortable family/highway usually sedan/suv; city driving prefers automatic; offroad/camping prefers suv/pickup; economic+luxury, economic+full-options, and family+hatchback are conflicts. Respect negations such as غیرلوکس, غیراقتصادی, بدون آپشن. Add a field only when requested or strongly implied. Do not invent a model, color, origin, year, or mileage. Types: city and transmission are strings; min/max price, year and mileage fields are integers (0 when unknown); every other key is an array of strings. Respond with a single JSON object and nothing else."

const comparisonInstructions = "You are a careful Persian used-car advisor. Compare only the supplied real listings. Never invent facts, prices, features, or IDs. Return JSON with recommendations (one object per listing, preserving IDs) and overall_recommendation. Each recommendation must have id, title, fit, tradeoffs (array), best_for. Mention uncertainty when source data is missing. Types: id is the integer listing id; title, fit and best_for are Persian strings; tradeoffs is an array of Persian strings; overall_recommendation is one Persian string. Respond with a single JSON object and nothing else."

func (o *OpenAI) responsesURL() string {
	baseURL := strings.TrimRight(strings.TrimSpace(o.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return baseURL + "/responses"
}

// respondJSON calls the Responses API and decodes the model's JSON answer into out.
// JSON mode is not requested because some gateways reject text.format, so the
// instructions must ask for JSON and the object is cut out of the reply text.
func (o *OpenAI) respondJSON(ctx context.Context, instructions, input string, out any) error {
	if o == nil || o.Key == "" {
		return errors.New("llm is not configured")
	}
	body := map[string]any{"model": o.Model, "instructions": instructions, "input": input}
	if o.ReasoningEffort != "" {
		body["reasoning"] = map[string]string{"effort": o.ReasoningEffort}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.responsesURL(), strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+o.Key)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("llm status %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return errors.New("llm response read failed")
	}
	text, err := responseText(raw)
	if err != nil {
		return err
	}
	return lenientUnmarshal([]byte(text), out)
}

// lenientUnmarshal decodes model JSON into out, coercing near-miss shapes such as a
// string where a list is expected or a list/object where a string is expected.
func lenientUnmarshal(data []byte, out any) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	fixed, err := json.Marshal(coerceJSON(value, reflect.TypeOf(out).Elem()))
	if err != nil {
		return err
	}
	return json.Unmarshal(fixed, out)
}

func coerceJSON(value any, t reflect.Type) any {
	switch t.Kind() {
	case reflect.String:
		return jsonText(value)
	case reflect.Int, reflect.Int64, reflect.Float64:
		switch v := value.(type) {
		case float64:
			return v
		case string:
			if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return n
			}
		}
		return nil
	case reflect.Slice:
		if value == nil {
			return nil
		}
		items, ok := value.([]any)
		if !ok {
			items = []any{value}
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, coerceJSON(item, t.Elem()))
		}
		return out
	case reflect.Struct:
		obj, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if v, ok := obj[name]; ok {
				obj[name] = coerceJSON(v, field.Type)
			}
		}
		return obj
	}
	return value
}

func jsonText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := jsonText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			if text := jsonText(v[key]); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprint(v)
	}
}

// responseText returns the JSON object inside a Responses API reply.
func responseText(raw []byte) (string, error) {
	var envelope struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", errors.New("invalid llm response")
	}
	text := envelope.OutputText
	if text == "" {
		var parts []string
		for _, item := range envelope.Output {
			if item.Type != "message" {
				continue
			}
			for _, content := range item.Content {
				if content.Type == "output_text" {
					parts = append(parts, content.Text)
				}
			}
		}
		text = strings.Join(parts, "")
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return "", errors.New("llm response contained no JSON object")
	}
	return text[start : end+1], nil
}

func (o *OpenAI) ExtractIntent(ctx context.Context, message string) (Intent, error) {
	var out Intent
	if err := o.respondJSON(ctx, intentInstructions, message, &out); err != nil {
		return Intent{}, err
	}
	return out, nil
}

func (o *OpenAI) ExplainComparison(ctx context.Context, items []Listing) ([]CompareRecommendation, string, error) {
	if len(items) == 0 || len(items) > 3 {
		return nil, "", fmt.Errorf("comparison is not available")
	}
	compact := make([]map[string]any, 0, len(items))
	for _, item := range items {
		compact = append(compact, map[string]any{
			"id": item.ID, "brand": item.Brand, "model": item.Model, "trim": item.Trim,
			"year": item.Year, "price_irr": item.PriceIRR, "mileage_km": item.MileageKM,
			"color": item.Color, "transmission": item.Transmission, "body_condition": item.BodyCondition,
			"features": item.Features, "description": item.Description,
		})
	}
	payload, err := json.Marshal(compact)
	if err != nil {
		return nil, "", err
	}
	var result CompareResponse
	if err := o.respondJSON(ctx, comparisonInstructions, string(payload), &result); err != nil {
		return nil, "", err
	}
	if len(result.Recommendations) != len(items) {
		return nil, "", fmt.Errorf("comparison response did not cover every listing")
	}
	valid := make(map[int64]bool, len(items))
	for _, item := range items {
		valid[item.ID] = true
	}
	seen := make(map[int64]bool, len(items))
	for _, recommendation := range result.Recommendations {
		if !valid[recommendation.ID] || seen[recommendation.ID] || strings.TrimSpace(recommendation.Fit) == "" || strings.TrimSpace(recommendation.BestFor) == "" {
			return nil, "", fmt.Errorf("comparison response contained an invalid recommendation")
		}
		seen[recommendation.ID] = true
	}
	overall := strings.TrimSpace(result.OverallRecommendation)
	if overall == "" {
		return nil, "", fmt.Errorf("comparison response had no overall recommendation")
	}
	return result.Recommendations, overall, nil
}
