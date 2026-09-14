// Package auth holds the authentication primitives of the wzap product
// surface: password hashing, instance API key minting, session JWTs and the
// request scope carried through handler contexts.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	// TokenLifetime is the validity of a minted session token. There are no
	// refresh tokens: the client logs in again at expiry.
	TokenLifetime = 12 * time.Hour
	// SessionCookieName is the cookie carrying the session token.
	SessionCookieName = "wzap_session"
)

// Scope kinds carried through request contexts.
const (
	ScopeGlobal   = "global"
	ScopeUser     = "user"
	ScopeInstance = "instance"
)

// Scope describes the identity a request acts as: a manager user session
// (Kind user), an instance API key (Kind instance) or the service token
// (Kind global).
type Scope struct {
	Kind       string
	UserID     uuid.UUID
	Role       string
	InstanceID uuid.UUID
}

type scopeKey struct{}

// ContextWithScope stores scope in ctx for downstream handlers.
func ContextWithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

// ScopeFromContext returns the scope stored by ContextWithScope, false when
// the context carries none.
func ScopeFromContext(ctx context.Context) (Scope, bool) {
	scope, ok := ctx.Value(scopeKey{}).(Scope)
	return scope, ok
}

// ErrForbidden reports that the scope in context lacks the required role or
// instance. Callers map it to 403.
var ErrForbidden = errors.New("forbidden")

// RequireRole reports whether scope may act with role: the global scope acts
// as admin everywhere, a user scope acts with exactly its own role, and an
// instance scope never satisfies a role.
func RequireRole(s Scope, role string) error {
	if s.Kind == ScopeGlobal {
		return nil
	}
	if s.Kind == ScopeUser && s.Role == role {
		return nil
	}
	return ErrForbidden
}

// RequireInstance reports whether scope may act on the instance id: the
// global scope reaches every instance, an instance scope reaches exactly its
// own instance, and a user scope is always denied here — user-to-own-instance
// authorization needs the instance owner from the database, which is handler
// logic, not this helper.
func RequireInstance(s Scope, id uuid.UUID) error {
	if s.Kind == ScopeGlobal {
		return nil
	}
	if s.Kind == ScopeInstance && s.InstanceID == id {
		return nil
	}
	return ErrForbidden
}

// HashPassword hashes password with bcrypt at the default cost.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// CheckPassword reports whether password matches the bcrypt hash.
func CheckPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// MintAPIKey mints a random instance key and its storage hash. The key is 32
// bytes of crypto randomness encoded base64url without padding so it stays
// header-safe; the hash is the lowercase hex of the SHA-256 over the key
// string and is what the repository stores.
func MintAPIKey() (key, hashHex string, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", fmt.Errorf("mint api key: %w", err)
	}
	key = base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(key))
	return key, hex.EncodeToString(sum[:]), nil
}

// sessionClaims is the JWT shape of a manager session: the user id in sub,
// the role alongside, expiring TokenLifetime after mint.
type sessionClaims struct {
	jwt.RegisteredClaims
	Role string `json:"role"`
}

// MintToken mints a session token for userID with role, signed HS256 under
// secret.
func MintToken(userID uuid.UUID, role, secret string) (string, error) {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, sessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TokenLifetime)),
		},
		Role: role,
	})
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("mint token: %w", err)
	}
	return signed, nil
}

// ParseToken verifies a session token signed under secret and returns its
// user session scope.
func ParseToken(token, secret string) (Scope, error) {
	var claims sessionClaims
	parsed, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return Scope{}, fmt.Errorf("parse token: %w", err)
	}
	if !parsed.Valid {
		return Scope{}, fmt.Errorf("parse token: invalid token")
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return Scope{}, fmt.Errorf("parse token: invalid subject: %w", err)
	}
	return Scope{Kind: ScopeUser, UserID: userID, Role: claims.Role}, nil
}
