package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSafeReturnPath(t *testing.T) {
	tests := map[string]string{
		"":                             "/register",
		"/login":                       "/register",
		"/register?aff=test":           "/register?aff=test",
		"/sign-up":                     "/sign-up",
		"/sign-in":                     "/register",
		"https://example.com/register": "/register",
		"//example.com/register":       "/register",
		"/api/user/login":              "/register",
	}

	for input, expected := range tests {
		if actual := safeReturnPath(input); actual != expected {
			t.Errorf("safeReturnPath(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestEmbeddedAltchaAssetChecksum(t *testing.T) {
	asset, err := embeddedFiles.ReadFile("static/altcha.i18n-3.2.1.min.js")
	if err != nil {
		t.Fatalf("read embedded ALTCHA asset: %v", err)
	}
	sum := sha256.Sum256(asset)
	const expected = "67a06fef795b716022fc0635346fb4a3ba433d8b8eb429044e2b7d14edd9bc4e"
	if actual := hex.EncodeToString(sum[:]); actual != expected {
		t.Fatalf("ALTCHA asset checksum = %s, want %s", actual, expected)
	}
}

func TestWithAffiliate(t *testing.T) {
	if actual := withAffiliate("/sign-up", "mini56"); actual != "/sign-up?aff=mini56" {
		t.Fatalf("withAffiliate() = %q", actual)
	}
	if actual := withAffiliate("/register", strings.Repeat("a", 129)); actual != "/register" {
		t.Fatalf("oversized affiliate should be ignored, got %q", actual)
	}
}

func TestTokenIsBoundToUserAgent(t *testing.T) {
	app := &gate{cfg: config{
		cookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		cookieTTL:    time.Minute,
	}}

	token, err := app.issueToken("browser-a")
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	if !app.validToken(token, "browser-a") {
		t.Fatal("token should be valid for the original User-Agent")
	}
	if app.validToken(token, "browser-b") {
		t.Fatal("token should be rejected for a different User-Agent")
	}
}

func TestSolutionValidationAndReplayProtection(t *testing.T) {
	app := &gate{
		cfg: config{
			altchaSecret: []byte("abcdef0123456789abcdef0123456789"),
			challengeTTL: time.Minute,
			maxNumber:    100000,
		},
		usedProof: make(map[string]time.Time),
	}

	number := 42000
	salt := "test-salt." + strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)
	sum := sha256.Sum256([]byte(salt + strconv.Itoa(number)))
	challenge := hex.EncodeToString(sum[:])
	payload := solutionPayload{
		Algorithm: "SHA-256",
		Challenge: challenge,
		Number:    number,
		Salt:      salt,
		Signature: hmacHex(app.cfg.altchaSecret, challenge),
	}

	if !app.validSolution(payload) {
		t.Fatal("valid solution was rejected")
	}
	if !app.consumeProof(payload.Signature) {
		t.Fatal("first proof use should succeed")
	}
	if app.consumeProof(payload.Signature) {
		t.Fatal("replayed proof should be rejected")
	}

	payload.Number++
	if app.validSolution(payload) {
		t.Fatal("tampered solution should be rejected")
	}
}
