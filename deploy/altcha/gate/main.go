package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const cookieName = "newapi_altcha_gate"

//go:embed templates/verify.html
var templateFiles embed.FS

type config struct {
	altchaSecret  []byte
	cookieSecret  []byte
	cookieSecure  bool
	cookieTTL     time.Duration
	challengeTTL  time.Duration
	maxNumber     int
	listenAddress string
}

type rateEntry struct {
	window time.Time
	count  int
}

type gate struct {
	cfg       config
	page      *template.Template
	rateMu    sync.Mutex
	rates     map[string]rateEntry
	usedMu    sync.Mutex
	usedProof map[string]time.Time
}

type solutionPayload struct {
	Algorithm string
	Challenge string
	Number    int
	Salt      string
	Signature string
}

type cookieClaims struct {
	Scope string
	Exp   int64
	JTI   string
	UA    string
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-healthcheck" {
		runHealthcheck()
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	page, err := template.ParseFS(templateFiles, "templates/verify.html")
	if err != nil {
		log.Fatalf("parse template: %v", err)
	}

	app := &gate{
		cfg:       cfg,
		page:      page,
		rates:     make(map[string]rateEntry),
		usedProof: make(map[string]time.Time),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", app.health)
	mux.HandleFunc("GET /verify", app.verifyPage)
	mux.HandleFunc("POST /verify", app.verifySolution)
	mux.HandleFunc("GET /challenge", app.challenge)
	mux.HandleFunc("GET /auth", app.auth)

	server := &http.Server{
		Addr:              cfg.listenAddress,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("ALTCHA gate listening on %s", cfg.listenAddress)
	log.Fatal(server.ListenAndServe())
}

func runHealthcheck() {
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://127.0.0.1:8080/health")
	if err != nil {
		os.Exit(1)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}

func loadConfig() (config, error) {
	altchaSecret := strings.TrimSpace(os.Getenv("ALTCHA_HMAC_SECRET"))
	cookieSecret := strings.TrimSpace(os.Getenv("COOKIE_SIGNING_SECRET"))
	if len(altchaSecret) < 32 || len(cookieSecret) < 32 {
		return config{}, errors.New("ALTCHA_HMAC_SECRET and COOKIE_SIGNING_SECRET must each be at least 32 characters")
	}

	maxNumber := envInt("ALTCHA_MAX_NUMBER", 100000)
	if maxNumber < 1000 || maxNumber > 5000000 {
		return config{}, errors.New("ALTCHA_MAX_NUMBER must be between 1000 and 5000000")
	}

	return config{
		altchaSecret:  []byte(altchaSecret),
		cookieSecret:  []byte(cookieSecret),
		cookieSecure:  envBool("COOKIE_SECURE", false),
		cookieTTL:     time.Duration(envInt("COOKIE_TTL_SECONDS", 300)) * time.Second,
		challengeTTL:  time.Duration(envInt("CHALLENGE_TTL_SECONDS", 120)) * time.Second,
		maxNumber:     maxNumber,
		listenAddress: envString("LISTEN_ADDRESS", ":8080"),
	}, nil
}

func (g *gate) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{\"ok\":true}"))
}

func (g *gate) verifyPage(w http.ResponseWriter, r *http.Request) {
	returnPath := safeReturnPath(r.URL.Query().Get("return"))
	returnPath = withAffiliate(returnPath, r.URL.Query().Get("aff"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := g.page.Execute(w, map[string]string{"ReturnPath": returnPath}); err != nil {
		log.Printf("render verification page: %v", err)
	}
}

func (g *gate) challenge(w http.ResponseWriter, r *http.Request) {
	if !g.allow(clientIP(r)+":challenge", 20, time.Minute) {
		writeJSONError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
		return
	}

	number, err := secureRandomInt(g.cfg.maxNumber)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "无法生成验证任务")
		return
	}
	saltBytes := make([]byte, 16)
	if _, err := rand.Read(saltBytes); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "无法生成验证任务")
		return
	}

	expiresAt := time.Now().Add(g.cfg.challengeTTL).Unix()
	salt := hex.EncodeToString(saltBytes) + "." + strconv.FormatInt(expiresAt, 10)
	challengeBytes := sha256.Sum256([]byte(salt + strconv.Itoa(number)))
	challenge := hex.EncodeToString(challengeBytes[:])
	signature := hmacHex(g.cfg.altchaSecret, challenge)

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"algorithm": "SHA-256",
		"challenge": challenge,
		"maxnumber": g.cfg.maxNumber,
		"salt":      salt,
		"signature": signature,
	})
}

func (g *gate) verifySolution(w http.ResponseWriter, r *http.Request) {
	if !g.allow(clientIP(r)+":verify", 10, time.Minute) {
		http.Error(w, "验证请求过于频繁，请稍后再试", http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "验证数据无效", http.StatusBadRequest)
		return
	}
	if r.FormValue("website") != "" {
		http.Error(w, "验证失败", http.StatusBadRequest)
		return
	}

	payload, err := decodeSolution(r.FormValue("altcha"))
	if err != nil || !g.validSolution(payload) {
		http.Error(w, "人机验证失败或已过期，请重试", http.StatusUnauthorized)
		return
	}
	if !g.consumeProof(payload.Signature) {
		http.Error(w, "该验证已使用，请重新验证", http.StatusUnauthorized)
		return
	}

	token, err := g.issueToken(r.UserAgent())
	if err != nil {
		http.Error(w, "无法签发验证凭证", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(g.cfg.cookieTTL.Seconds()),
		Expires:  time.Now().Add(g.cfg.cookieTTL),
		HttpOnly: true,
		Secure:   g.cfg.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, safeReturnPath(r.FormValue("return")), http.StatusSeeOther)
}

func (g *gate) auth(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || !g.validToken(cookie.Value, r.UserAgent()) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (g *gate) validSolution(payload solutionPayload) bool {
	if payload.Algorithm != "SHA-256" || payload.Number < 0 || payload.Number > g.cfg.maxNumber {
		return false
	}

	separator := strings.LastIndexByte(payload.Salt, '.')
	if separator < 1 {
		return false
	}
	expiresAt, err := strconv.ParseInt(payload.Salt[separator+1:], 10, 64)
	if err != nil || time.Now().Unix() > expiresAt {
		return false
	}

	challengeBytes := sha256.Sum256([]byte(payload.Salt + strconv.Itoa(payload.Number)))
	expectedChallenge := hex.EncodeToString(challengeBytes[:])
	if !secureHexEqual(payload.Challenge, expectedChallenge) {
		return false
	}

	expectedSignature := hmacHex(g.cfg.altchaSecret, payload.Challenge)
	return secureHexEqual(payload.Signature, expectedSignature)
}

func (g *gate) consumeProof(signature string) bool {
	now := time.Now()
	g.usedMu.Lock()
	defer g.usedMu.Unlock()

	for key, expiresAt := range g.usedProof {
		if now.After(expiresAt) {
			delete(g.usedProof, key)
		}
	}
	if _, exists := g.usedProof[signature]; exists {
		return false
	}
	g.usedProof[signature] = now.Add(g.cfg.challengeTTL)
	return true
}

func (g *gate) issueToken(userAgent string) (string, error) {
	jtiBytes := make([]byte, 16)
	if _, err := rand.Read(jtiBytes); err != nil {
		return "", err
	}
	claims := map[string]any{
		"scope": "auth",
		"exp":   time.Now().Add(g.cfg.cookieTTL).Unix(),
		"jti":   hex.EncodeToString(jtiBytes),
		"ua":    userAgentDigest(userAgent),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signature := hmacBytes(g.cfg.cookieSecret, encodedPayload)
	return encodedPayload + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (g *gate) validToken(token, userAgent string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, hmacBytes(g.cfg.cookieSecret, parts[0])) {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}

	var claims cookieClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return false
	}
	return claims.Scope == "auth" &&
		claims.Exp >= time.Now().Unix() &&
		claims.JTI != "" &&
		hmac.Equal([]byte(claims.UA), []byte(userAgentDigest(userAgent)))
}

func (g *gate) allow(key string, limit int, duration time.Duration) bool {
	now := time.Now()
	g.rateMu.Lock()
	defer g.rateMu.Unlock()

	entry := g.rates[key]
	if entry.window.IsZero() || now.Sub(entry.window) >= duration {
		g.rates[key] = rateEntry{window: now, count: 1}
		return true
	}
	if entry.count >= limit {
		return false
	}
	entry.count++
	g.rates[key] = entry
	return true
}

func decodeSolution(value string) (solutionPayload, error) {
	var payload solutionPayload
	if value == "" {
		return payload, errors.New("missing solution")
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(value)
	}
	if err != nil {
		return payload, err
	}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return payload, err
	}
	return payload, nil
}

func safeReturnPath(raw string) string {
	if raw == "" {
		return "/register"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return "/register"
	}
	switch parsed.Path {
	case "/register", "/sign-up":
		return parsed.RequestURI()
	default:
		return "/register"
	}
}

func withAffiliate(returnPath, affiliate string) string {
	affiliate = strings.TrimSpace(affiliate)
	if affiliate == "" || len(affiliate) > 128 {
		return returnPath
	}
	parsed, err := url.Parse(returnPath)
	if err != nil {
		return returnPath
	}
	query := parsed.Query()
	query.Set("aff", affiliate)
	parsed.RawQuery = query.Encode()
	return parsed.RequestURI()
}

func clientIP(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Real-IP")); value != "" {
		return value
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func secureRandomInt(max int) (int, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(int64(max)+1))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}

func userAgentDigest(userAgent string) string {
	sum := sha256.Sum256([]byte(userAgent))
	return hex.EncodeToString(sum[:16])
}

func hmacHex(secret []byte, value string) string {
	return hex.EncodeToString(hmacBytes(secret, value))
}

func hmacBytes(secret []byte, value string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func secureHexEqual(left, right string) bool {
	leftBytes, leftErr := hex.DecodeString(left)
	rightBytes, rightErr := hex.DecodeString(right)
	return leftErr == nil && rightErr == nil && hmac.Equal(leftBytes, rightBytes)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": message})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func envString(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
