package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"askflow/internal/config"

	"golang.org/x/oauth2"
)

// testProviders returns a standard set of provider configs for testing.
func testProviders() map[string]config.OAuthProviderConfig {
	return map[string]config.OAuthProviderConfig{
		ProviderGoogle: {
			ClientID:     "google-client-id",
			ClientSecret: "google-client-secret",
			AuthURL:      "https://accounts.google.com/o/oauth2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			RedirectURL:  "http://localhost:8080/callback/google",
			Scopes:       []string{"openid", "email", "profile"},
		},
		ProviderFacebook: {
			ClientID:     "fb-client-id",
			ClientSecret: "fb-client-secret",
			AuthURL:      "https://www.facebook.com/v18.0/dialog/oauth",
			TokenURL:     "https://graph.facebook.com/v18.0/oauth/access_token",
			RedirectURL:  "http://localhost:8080/callback/facebook",
			Scopes:       []string{"email", "public_profile"},
		},
		ProviderAmazon: {
			ClientID:     "amazon-client-id",
			ClientSecret: "amazon-client-secret",
			AuthURL:      "https://www.amazon.com/ap/oa",
			TokenURL:     "https://api.amazon.com/auth/o2/token",
			RedirectURL:  "http://localhost:8080/callback/amazon",
			Scopes:       []string{"profile"},
		},
		ProviderApple: {
			ClientID:     "apple-client-id",
			ClientSecret: "apple-client-secret",
			AuthURL:      "https://appleid.apple.com/auth/authorize",
			TokenURL:     "https://appleid.apple.com/auth/token",
			RedirectURL:  "http://localhost:8080/callback/apple",
			Scopes:       []string{"name", "email"},
		},
	}
}

func TestNewOAuthClient(t *testing.T) {
	providers := testProviders()
	client := NewOAuthClient(providers)

	if client == nil {
		t.Fatal("expected non-nil OAuthClient")
	}
	if len(client.providers) != 4 {
		t.Errorf("expected 4 providers, got %d", len(client.providers))
	}
	for _, name := range []string{ProviderGoogle, ProviderFacebook, ProviderAmazon, ProviderApple} {
		if _, ok := client.providers[name]; !ok {
			t.Errorf("expected provider %s to be configured", name)
		}
	}
}

func TestGetAuthURL_AllProviders(t *testing.T) {
	providers := testProviders()
	client := NewOAuthClient(providers)

	tests := []struct {
		provider    string
		wantClientID string
	}{
		{ProviderGoogle, "google-client-id"},
		{ProviderFacebook, "fb-client-id"},
		{ProviderAmazon, "amazon-client-id"},
		{ProviderApple, "apple-client-id"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			authURL, err := client.GetAuthURL(tt.provider)
			if err != nil {
				t.Fatalf("GetAuthURL(%s) error: %v", tt.provider, err)
			}
			parsed, err := url.Parse(authURL)
			if err != nil {
				t.Fatalf("invalid URL: %v", err)
			}
			q := parsed.Query()
			if got := q.Get("client_id"); got != tt.wantClientID {
				t.Errorf("client_id = %q, want %q", got, tt.wantClientID)
			}
			if got := q.Get("redirect_uri"); got != providers[tt.provider].RedirectURL {
				t.Errorf("redirect_uri = %q, want %q", got, providers[tt.provider].RedirectURL)
			}
			if got := q.Get("response_type"); got != "code" {
				t.Errorf("response_type = %q, want %q", got, "code")
			}
			if got := q.Get("state"); got == "" {
				t.Error("expected non-empty state parameter")
			}
		})
	}
}

func TestGetAuthURL_UnsupportedProvider(t *testing.T) {
	client := NewOAuthClient(testProviders())
	_, err := client.GetAuthURL("github")
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
}

func TestGetAuthURL_ContainsScopes(t *testing.T) {
	client := NewOAuthClient(testProviders())
	authURL, err := client.GetAuthURL(ProviderGoogle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parsed, _ := url.Parse(authURL)
	scope := parsed.Query().Get("scope")
	if scope == "" {
		t.Error("expected scope parameter in auth URL")
	}
	// Google scopes should include openid, email, profile
	for _, s := range []string{"openid", "email", "profile"} {
		if !containsScope(scope, s) {
			t.Errorf("scope %q not found in %q", s, scope)
		}
	}
}

func containsScope(scopeStr, target string) bool {
	for _, s := range splitScopes(scopeStr) {
		if s == target {
			return true
		}
	}
	return false
}

func splitScopes(s string) []string {
	// oauth2 library joins scopes with space
	var result []string
	for _, part := range splitBySpace(s) {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func splitBySpace(s string) []string {
	var parts []string
	current := ""
	for _, c := range s {
		if c == ' ' || c == '+' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

func TestHandleCallback_UnsupportedProvider(t *testing.T) {
	client := NewOAuthClient(testProviders())
	_, err := client.HandleCallback("github", "some-code")
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
}

func TestParseUserInfo_Google(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"id":    "google-123",
		"email": "user@gmail.com",
		"name":  "Test User",
	})
	user, err := parseUserInfo(ProviderGoogle, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID != "google-123" {
		t.Errorf("ID = %q, want %q", user.ID, "google-123")
	}
	if user.Email != "user@gmail.com" {
		t.Errorf("Email = %q, want %q", user.Email, "user@gmail.com")
	}
	if user.Name != "Test User" {
		t.Errorf("Name = %q, want %q", user.Name, "Test User")
	}
	if user.Provider != ProviderGoogle {
		t.Errorf("Provider = %q, want %q", user.Provider, ProviderGoogle)
	}
}

func TestParseUserInfo_Facebook(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"id":    "fb-456",
		"email": "user@facebook.com",
		"name":  "FB User",
	})
	user, err := parseUserInfo(ProviderFacebook, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID != "fb-456" || user.Email != "user@facebook.com" || user.Name != "FB User" {
		t.Errorf("unexpected user: %+v", user)
	}
}

func TestParseUserInfo_Amazon(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"user_id": "amzn-789",
		"email":   "user@amazon.com",
		"name":    "Amazon User",
	})
	user, err := parseUserInfo(ProviderAmazon, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID != "amzn-789" {
		t.Errorf("ID = %q, want %q", user.ID, "amzn-789")
	}
	if user.Email != "user@amazon.com" {
		t.Errorf("Email = %q, want %q", user.Email, "user@amazon.com")
	}
}

func TestParseUserInfo_InvalidJSON(t *testing.T) {
	_, err := parseUserInfo(ProviderGoogle, []byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseUserInfo_UnsupportedProvider(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{"id": "1"})
	_, err := parseUserInfo("github", body)
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
}

func TestDecodeJWTClaims(t *testing.T) {
	// Build a minimal JWT with known claims
	claims := map[string]interface{}{
		"sub":   "apple-user-001",
		"email": "user@icloud.com",
		"name":  "Apple User",
	}
	payload, _ := json.Marshal(claims)
	encoded := base64URLEncode(payload)
	jwt := "eyJhbGciOiJSUzI1NiJ9." + encoded + ".fake-signature"

	decoded, err := decodeJWTClaims(jwt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decoded["sub"] != "apple-user-001" {
		t.Errorf("sub = %v, want %q", decoded["sub"], "apple-user-001")
	}
	if decoded["email"] != "user@icloud.com" {
		t.Errorf("email = %v, want %q", decoded["email"], "user@icloud.com")
	}
}

func TestDecodeJWTClaims_InvalidFormat(t *testing.T) {
	_, err := decodeJWTClaims("not-a-jwt")
	if err == nil {
		t.Fatal("expected error for invalid JWT format")
	}
}

func TestDecodeJWTClaims_InvalidPayload(t *testing.T) {
	_, err := decodeJWTClaims("header.!!!invalid!!!.signature")
	if err == nil {
		t.Fatal("expected error for invalid base64 payload")
	}
}

// base64URLEncode encodes bytes to base64url without padding (for test JWT construction).
func base64URLEncode(data []byte) string {
	s := ""
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for i := 0; i < len(data); i += 3 {
		var b0, b1, b2 byte
		b0 = data[i]
		if i+1 < len(data) {
			b1 = data[i+1]
		}
		if i+2 < len(data) {
			b2 = data[i+2]
		}
		s += string(enc[(b0>>2)&0x3F])
		s += string(enc[((b0<<4)|(b1>>4))&0x3F])
		if i+1 < len(data) {
			s += string(enc[((b1<<2)|(b2>>6))&0x3F])
		}
		if i+2 < len(data) {
			s += string(enc[b2&0x3F])
		}
	}
	// Add padding for base64url
	switch len(data) % 3 {
	case 1:
		s += "=="
	case 2:
		s += "="
	}
	return s
}

func TestFetchUserInfo_Google(t *testing.T) {
	// Set up a mock HTTP server that returns Google userinfo
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Authorization header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-access-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    "google-user-1",
			"email": "test@gmail.com",
			"name":  "Google Test",
		})
	}))
	defer server.Close()

	// Temporarily override the userinfo URL
	origURL := providerUserInfoURLs[ProviderGoogle]
	providerUserInfoURLs[ProviderGoogle] = server.URL
	defer func() { providerUserInfoURLs[ProviderGoogle] = origURL }()

	client := NewOAuthClient(testProviders())
	client.httpClient = server.Client()

	token := &oauth2.Token{AccessToken: "test-access-token"}
	user, err := client.fetchUserInfo(ProviderGoogle, token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID != "google-user-1" || user.Email != "test@gmail.com" || user.Name != "Google Test" {
		t.Errorf("unexpected user: %+v", user)
	}
	if user.Provider != ProviderGoogle {
		t.Errorf("Provider = %q, want %q", user.Provider, ProviderGoogle)
	}
}

func TestFetchUserInfo_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer server.Close()

	origURL := providerUserInfoURLs[ProviderGoogle]
	providerUserInfoURLs[ProviderGoogle] = server.URL
	defer func() { providerUserInfoURLs[ProviderGoogle] = origURL }()

	client := NewOAuthClient(testProviders())
	client.httpClient = server.Client()

	token := &oauth2.Token{AccessToken: "test-token"}
	_, err := client.fetchUserInfo(ProviderGoogle, token)
	if err == nil {
		t.Fatal("expected error for server error response")
	}
}

func TestFetchUserInfo_NoUserInfoURL(t *testing.T) {
	client := NewOAuthClient(testProviders())
	token := &oauth2.Token{AccessToken: "test-token"}
	_, err := client.fetchUserInfo("unknown-provider", token)
	if err == nil {
		t.Fatal("expected error for provider without userinfo URL")
	}
}

func TestStringVal(t *testing.T) {
	m := map[string]interface{}{
		"str":    "hello",
		"num":    42,
		"absent": nil,
	}
	if got := stringVal(m, "str"); got != "hello" {
		t.Errorf("stringVal(str) = %q, want %q", got, "hello")
	}
	if got := stringVal(m, "num"); got != "42" {
		t.Errorf("stringVal(num) = %q, want %q", got, "42")
	}
	if got := stringVal(m, "missing"); got != "" {
		t.Errorf("stringVal(missing) = %q, want empty", got)
	}
}

func TestNewOAuthClient_EmptyProviders(t *testing.T) {
	client := NewOAuthClient(map[string]config.OAuthProviderConfig{})
	if client == nil {
		t.Fatal("expected non-nil client even with empty providers")
	}
	if len(client.providers) != 0 {
		t.Errorf("expected 0 providers, got %d", len(client.providers))
	}
}
