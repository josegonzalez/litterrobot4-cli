// Package cloud talks to Litter-Robot 4 units with stock firmware through
// Whisker's cloud API: AWS Cognito for sign in and an AppSync GraphQL
// endpoint for state and commands. The API is not published; the details
// follow pylitterbot, the library behind the Home Assistant integration.
package cloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Cognito settings used by the Whisker app.
const (
	CognitoEndpoint = "https://cognito-idp.us-east-1.amazonaws.com/"
	CognitoClientID = "4552ujeu3aic90nf8qn53levmn"
)

// Tokens are the Cognito tokens for a signed in account.
type Tokens struct {
	IDToken      string    `json:"idToken"`
	AccessToken  string    `json:"accessToken,omitempty"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	Expiry       time.Time `json:"expiry"`
}

// AuthError is returned when Cognito rejects the credentials or token.
type AuthError struct {
	Type    string
	Message string
}

func (e *AuthError) Error() string {
	if e.Message == "" {
		return "whisker sign in failed: " + e.Type
	}
	return "whisker sign in failed: " + e.Message
}

// Auth signs in to Cognito and keeps the ID token fresh.
type Auth struct {
	// HTTPClient is used for Cognito requests. http.DefaultClient when nil.
	HTTPClient *http.Client
	// Endpoint overrides CognitoEndpoint, for tests.
	Endpoint string
	// Email and Password are used to sign in again when the refresh token
	// stops working. Password may be empty.
	Email    string
	Password string
	// OnRefresh is called with new tokens, so they can be saved.
	OnRefresh func(Tokens)

	mu     sync.Mutex
	tokens Tokens
}

// NewAuth returns an Auth that starts from saved tokens.
func NewAuth(tokens Tokens) *Auth {
	return &Auth{tokens: tokens}
}

// Login signs in with an email and password.
func (a *Auth) Login(ctx context.Context, email, password string) (Tokens, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.initiate(ctx, "USER_PASSWORD_AUTH", map[string]string{
		"USERNAME": email,
		"PASSWORD": password,
	})
	if err != nil {
		return Tokens{}, err
	}
	a.Email = email
	a.tokens = t
	return t, nil
}

// Tokens returns the current tokens.
func (a *Auth) Tokens() Tokens {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tokens
}

// IDToken returns a valid ID token, refreshing it when it is about to
// expire.
func (a *Auth) IDToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tokens.IDToken != "" && time.Until(a.tokens.Expiry) > 30*time.Second {
		return a.tokens.IDToken, nil
	}
	if err := a.refreshLocked(ctx); err != nil {
		return "", err
	}
	return a.tokens.IDToken, nil
}

// Refresh gets a new ID token now.
func (a *Auth) Refresh(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refreshLocked(ctx)
}

func (a *Auth) refreshLocked(ctx context.Context) error {
	var err error
	if a.tokens.RefreshToken != "" {
		var t Tokens
		t, err = a.initiate(ctx, "REFRESH_TOKEN_AUTH", map[string]string{
			"REFRESH_TOKEN": a.tokens.RefreshToken,
		})
		if err == nil {
			if t.RefreshToken == "" {
				t.RefreshToken = a.tokens.RefreshToken
			}
			a.tokens = t
			if a.OnRefresh != nil {
				a.OnRefresh(t)
			}
			return nil
		}
		var ae *AuthError
		if !errors.As(err, &ae) {
			return err
		}
	}
	if a.Email == "" || a.Password == "" {
		if err != nil {
			return fmt.Errorf("%w; sign in again with: litterrobot4-cli login", err)
		}
		return errors.New("not signed in; run: litterrobot4-cli login")
	}
	t, err := a.initiate(ctx, "USER_PASSWORD_AUTH", map[string]string{
		"USERNAME": a.Email,
		"PASSWORD": a.Password,
	})
	if err != nil {
		return err
	}
	a.tokens = t
	if a.OnRefresh != nil {
		a.OnRefresh(t)
	}
	return nil
}

func (a *Auth) initiate(ctx context.Context, flow string, params map[string]string) (Tokens, error) {
	body, err := json.Marshal(map[string]any{
		"AuthFlow":       flow,
		"ClientId":       CognitoClientID,
		"AuthParameters": params,
	})
	if err != nil {
		return Tokens{}, err
	}
	endpoint := a.Endpoint
	if endpoint == "" {
		endpoint = CognitoEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService.InitiateAuth")

	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Tokens{}, err
	}

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Type    string `json:"__type"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Type == "" {
			return Tokens{}, fmt.Errorf("cognito: %s", resp.Status)
		}
		return Tokens{}, &AuthError{Type: e.Type, Message: e.Message}
	}

	var out struct {
		AuthenticationResult *struct {
			IDToken      string `json:"IdToken"`
			AccessToken  string `json:"AccessToken"`
			RefreshToken string `json:"RefreshToken"`
			ExpiresIn    int    `json:"ExpiresIn"`
		} `json:"AuthenticationResult"`
		ChallengeName string `json:"ChallengeName"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Tokens{}, fmt.Errorf("cognito: %w", err)
	}
	if out.AuthenticationResult == nil {
		if out.ChallengeName != "" {
			return Tokens{}, fmt.Errorf("cognito asked for %s, which is not supported", out.ChallengeName)
		}
		return Tokens{}, errors.New("cognito: no tokens in response")
	}
	r := out.AuthenticationResult
	expiry := time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)
	if exp, err := jwtExpiry(r.IDToken); err == nil {
		expiry = exp
	}
	return Tokens{
		IDToken:      r.IDToken,
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		Expiry:       expiry,
	}, nil
}

// jwtClaims decodes a JWT payload without checking its signature.
func jwtClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(b, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func jwtExpiry(token string) (time.Time, error) {
	claims, err := jwtClaims(token)
	if err != nil {
		return time.Time{}, err
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		return time.Time{}, errors.New("token has no exp claim")
	}
	return time.Unix(int64(exp), 0), nil
}

// UserID returns the Whisker user ID (the "mid" claim) from an ID token.
func UserID(idToken string) (string, error) {
	claims, err := jwtClaims(idToken)
	if err != nil {
		return "", err
	}
	switch v := claims["mid"].(type) {
	case string:
		return v, nil
	case float64:
		return fmt.Sprintf("%.0f", v), nil
	}
	return "", errors.New("token has no mid claim")
}
