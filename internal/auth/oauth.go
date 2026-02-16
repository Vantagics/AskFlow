// Package auth provides OAuth authentication and session management.
// It supports Google, Apple, Amazon, and Facebook OAuth providers.
package auth

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"askflow/internal/config"

	"golang.org/x/oauth2"
)

// Supported provider names.
const (
	ProviderGoogle   = "google"
	ProviderApple    = "apple"
	ProviderAmazon   = "amazon"
	ProviderFacebook = "facebook"
)

// Provider-specific userinfo endpoints.
var providerUserInfoURLs = map[string]string{
	ProviderGoogle:   "https://www.googleapis.com/oauth2/v2/userinfo",
	ProviderFacebook: "https://graph.facebook.com/me?fields=id,name,email",
	ProviderAmazon:   "https://api.amazon.com/user/profile",
}

// OAuthClient manages OAuth2 flows for multiple providers.
type OAuthClient struct {
	providers map[string]*oauth2.Config
	// httpClient is used for fetching user info. If nil, http.DefaultClient is used.
	httpClient *http.Client
	// pendingStates stores generated OAuth states for CSRF validation.
	// Maps state string -> expiry time.
	stateMu      sync.Mutex
	pendingStates map[string]time.Time
}

// OAuthUser represents a user authenticated via OAuth.
type OAuthUser struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

// NewOAuthClient creates an OAuthClient from the given provider configurations.
func NewOAuthClient(providers map[string]config.OAuthProviderConfig) *OAuthClient {
	configs := make(map[string]*oauth2.Config, len(providers))
	for name, p := range providers {
		configs[name] = &oauth2.Config{
			ClientID:     p.ClientID,
			ClientSecret: p.ClientSecret,
			Endpoint: oauth2.Endpoint{
				AuthURL:  p.AuthURL,
				TokenURL: p.TokenURL,
			},
			RedirectURL: p.RedirectURL,
			Scopes:      p.Scopes,
		}
	}
	oc := &OAuthClient{
		providers:     configs,
		pendingStates: make(map[string]time.Time),
	}
	// Background cleanup of expired states
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[OAuth] panic in state cleanup goroutine: %v", r)
			}
		}()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			oc.cleanExpiredStates()
		}
	}()
	return oc
}

// cleanExpiredStates removes expired OAuth state entries.
func (oc *OAuthClient) cleanExpiredStates() {
	oc.stateMu.Lock()
	defer oc.stateMu.Unlock()
	now := time.Now()
	for state, expiry := range oc.pendingStates {
		if now.After(expiry) {
			delete(oc.pendingStates, state)
		}
	}
}

// GetAuthURL returns the OAuth2 authorization URL for the given provider.
// A cryptographically random state parameter is generated to prevent CSRF attacks.
// The state is stored for validation during callback.
func (oc *OAuthClient) GetAuthURL(provider string) (string, error) {
	cfg, ok := oc.providers[provider]
	if !ok {
		return "", fmt.Errorf("unsupported OAuth provider: %s", provider)
	}
	// Generate a cryptographically random state to prevent CSRF
	stateBytes := make([]byte, 16)
	if _, err := io.ReadFull(cryptorand.Reader, stateBytes); err != nil {
		return "", fmt.Errorf("failed to generate OAuth state: %w", err)
	}
	state := fmt.Sprintf("%x", stateBytes)

	// Store state for validation (expires in 10 minutes)
	oc.stateMu.Lock()
	oc.pendingStates[state] = time.Now().Add(10 * time.Minute)
	// Limit stored states to prevent memory exhaustion
	if len(oc.pendingStates) > 10000 {
		for k := range oc.pendingStates {
			delete(oc.pendingStates, k)
			if len(oc.pendingStates) <= 5000 {
				break
			}
		}
	}
	oc.stateMu.Unlock()

	url := cfg.AuthCodeURL(state, oauth2.AccessTypeOnline)
	return url, nil
}

// ValidateState checks if the given state was previously generated and is still valid.
// Returns true if valid (and consumes the state), false otherwise.
func (oc *OAuthClient) ValidateState(state string) bool {
	oc.stateMu.Lock()
	defer oc.stateMu.Unlock()
	expiry, ok := oc.pendingStates[state]
	if !ok {
		return false
	}
	delete(oc.pendingStates, state) // one-time use
	return time.Now().Before(expiry)
}

// HandleCallback exchanges the authorization code for a token and fetches user info.
func (oc *OAuthClient) HandleCallback(provider string, code string) (*OAuthUser, error) {
	cfg, ok := oc.providers[provider]
	if !ok {
		return nil, fmt.Errorf("unsupported OAuth provider: %s", provider)
	}

	ctx := context.Background()
	token, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("OAuth token exchange failed for %s: %w", provider, err)
	}

	if provider == ProviderApple {
		return oc.handleAppleUser(token)
	}

	return oc.fetchUserInfo(provider, token)
}

// fetchUserInfo retrieves user profile from the provider's userinfo endpoint.
func (oc *OAuthClient) fetchUserInfo(provider string, token *oauth2.Token) (*OAuthUser, error) {
	userInfoURL, ok := providerUserInfoURLs[provider]
	if !ok {
		return nil, fmt.Errorf("no userinfo URL configured for provider: %s", provider)
	}

	client := oc.getHTTPClient()
	req, err := http.NewRequest("GET", userInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch userinfo from %s: %w", provider, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("userinfo request to %s returned status %d: %s", provider, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("read userinfo response from %s: %w", provider, err)
	}

	return parseUserInfo(provider, body)
}

// parseUserInfo parses the JSON response from a provider's userinfo endpoint.
func parseUserInfo(provider string, body []byte) (*OAuthUser, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse userinfo JSON from %s: %w", provider, err)
	}

	user := &OAuthUser{Provider: provider}

	switch provider {
	case ProviderGoogle:
		user.ID = stringVal(raw, "id")
		user.Email = stringVal(raw, "email")
		user.Name = stringVal(raw, "name")
	case ProviderFacebook:
		user.ID = stringVal(raw, "id")
		user.Email = stringVal(raw, "email")
		user.Name = stringVal(raw, "name")
	case ProviderAmazon:
		user.ID = stringVal(raw, "user_id")
		user.Email = stringVal(raw, "email")
		user.Name = stringVal(raw, "name")
	default:
		return nil, fmt.Errorf("unsupported provider for userinfo parsing: %s", provider)
	}

	return user, nil
}

// handleAppleUser extracts user info from Apple's ID token (JWT claims).
func (oc *OAuthClient) handleAppleUser(token *oauth2.Token) (*OAuthUser, error) {
	idToken, ok := token.Extra("id_token").(string)
	if !ok || idToken == "" {
		return nil, fmt.Errorf("Apple OAuth: no id_token in token response")
	}

	claims, err := decodeJWTClaims(idToken)
	if err != nil {
		return nil, fmt.Errorf("Apple OAuth: decode id_token: %w", err)
	}

	// Validate issuer claim
	iss := stringVal(claims, "iss")
	if iss != "https://appleid.apple.com" {
		return nil, fmt.Errorf("Apple OAuth: invalid issuer: %s", iss)
	}

	// Validate audience matches our client ID
	cfg, ok := oc.providers[ProviderApple]
	if !ok {
		return nil, fmt.Errorf("Apple OAuth: provider not configured")
	}
	aud := stringVal(claims, "aud")
	if aud != cfg.ClientID {
		return nil, fmt.Errorf("Apple OAuth: audience mismatch")
	}

	// Validate token is not expired
	if exp, ok := claims["exp"].(float64); ok {
		if time.Now().Unix() > int64(exp) {
			return nil, fmt.Errorf("Apple OAuth: id_token expired")
		}
	}

	user := &OAuthUser{
		Provider: ProviderApple,
		ID:       stringVal(claims, "sub"),
		Email:    stringVal(claims, "email"),
		Name:     stringVal(claims, "name"),
	}

	return user, nil
}

// decodeJWTClaims decodes the payload of a JWT without verifying the signature.
// This is a simplified implementation for extracting user claims from Apple's ID token.
func decodeJWTClaims(tokenString string) (map[string]interface{}, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT: expected 3 parts, got %d", len(parts))
	}

	payload := parts[1]
	// Add padding if needed
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decode JWT payload: %w", err)
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, fmt.Errorf("parse JWT claims: %w", err)
	}

	return claims, nil
}

// stringVal safely extracts a string value from a map.
func stringVal(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	return s
}

// getHTTPClient returns the configured HTTP client or a default one with timeout.
func (oc *OAuthClient) getHTTPClient() *http.Client {
	if oc.httpClient != nil {
		return oc.httpClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}
