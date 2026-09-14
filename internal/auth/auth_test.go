package auth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"wzap/internal/auth"
)

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := auth.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" || hash == "correct-horse-battery" {
		t.Errorf("HashPassword returned a non-hash %q", hash)
	}

	if err := auth.CheckPassword(hash, "correct-horse-battery"); err != nil {
		t.Errorf("CheckPassword with the right password: %v", err)
	}
	if err := auth.CheckPassword(hash, "wrong-password"); err == nil {
		t.Error("CheckPassword with the wrong password = nil, want an error")
	}
}

func TestHashPasswordUsesSalt(t *testing.T) {
	first, err := auth.HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := auth.HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Error("two hashes of the same password are equal, want a random salt")
	}
}

func TestMintAPIKey(t *testing.T) {
	key, hashHex, err := auth.MintAPIKey()
	if err != nil {
		t.Fatalf("MintAPIKey: %v", err)
	}

	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		t.Fatalf("key %q is not base64url without padding: %v", key, err)
	}
	if len(raw) != 32 {
		t.Errorf("key decodes to %d bytes, want 32", len(raw))
	}

	sum := sha256.Sum256([]byte(key))
	if want := hex.EncodeToString(sum[:]); hashHex != want {
		t.Errorf("hash = %q, want hex sha256 %q", hashHex, want)
	}
	if len(hashHex) != 64 || strings.ToLower(hashHex) != hashHex {
		t.Errorf("hash %q is not lowercase hex", hashHex)
	}

	other, otherHash, err := auth.MintAPIKey()
	if err != nil {
		t.Fatalf("MintAPIKey: %v", err)
	}
	if other == key || otherHash == hashHex {
		t.Error("two minted keys are equal, want fresh randomness")
	}
}

func TestMintParseTokenRoundtrip(t *testing.T) {
	id := uuid.New()

	token, err := auth.MintToken(id, "admin", "test-secret")
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if token == "" {
		t.Fatal("MintToken returned an empty token")
	}

	scope, err := auth.ParseToken(token, "test-secret")
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if scope.Kind != auth.ScopeUser {
		t.Errorf("scope kind = %q, want %q", scope.Kind, auth.ScopeUser)
	}
	if scope.UserID != id {
		t.Errorf("scope user = %s, want %s", scope.UserID, id)
	}
	if scope.Role != "admin" {
		t.Errorf("scope role = %q, want %q", scope.Role, "admin")
	}
}

func TestMintedTokenExpiresInTwelveHours(t *testing.T) {
	before := time.Now()

	token, err := auth.MintToken(uuid.New(), "user", "test-secret")
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}

	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		t.Fatalf("parse unverified: %v", err)
	}
	exp, err := claims.GetExpirationTime()
	if err != nil {
		t.Fatalf("exp claim: %v", err)
	}
	if exp == nil {
		t.Fatal("token has no exp claim")
	}
	if got, want := exp.Sub(before), 12*time.Hour; got < want-time.Minute || got > want+time.Minute {
		t.Errorf("token lifetime = %v, want ~12h", got)
	}
	if sub, _ := claims.GetSubject(); sub == "" {
		t.Error("token has no sub claim")
	}
}

func TestParseTokenRejects(t *testing.T) {
	token, err := auth.MintToken(uuid.New(), "admin", "right-secret")
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}

	for _, tt := range []struct {
		name  string
		token string
		// secret used to verify.
		secret string
	}{
		{name: "wrong secret", token: token, secret: "wrong-secret"},
		{name: "malformed", token: "not-a-token", secret: "right-secret"},
		{name: "tampered", token: token[:len(token)-1] + "x", secret: "right-secret"},
		{name: "empty", token: "", secret: "right-secret"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := auth.ParseToken(tt.token, tt.secret); err == nil {
				t.Error("ParseToken = nil error, want rejection")
			}
		})
	}
}

func TestParseTokenRejectsExpired(t *testing.T) {
	expired := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  uuid.NewString(),
		"role": "admin",
		"exp":  time.Now().Add(-time.Hour).Unix(),
	})
	token, err := expired.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}

	if _, err := auth.ParseToken(token, "test-secret"); err == nil {
		t.Error("ParseToken of an expired token = nil error, want rejection")
	}
}

func TestScopeContextRoundtrip(t *testing.T) {
	scope := auth.Scope{Kind: auth.ScopeUser, UserID: uuid.New(), Role: "admin"}

	got, ok := auth.ScopeFromContext(auth.ContextWithScope(t.Context(), scope))
	if !ok {
		t.Fatal("ScopeFromContext = false, want the stored scope")
	}
	if got != scope {
		t.Errorf("scope = %+v, want %+v", got, scope)
	}
}

func TestScopeFromContextAbsent(t *testing.T) {
	if _, ok := auth.ScopeFromContext(t.Context()); ok {
		t.Error("ScopeFromContext on a bare context = true, want false")
	}
}

func TestRequireRole(t *testing.T) {
	adminID := uuid.New()
	instanceID := uuid.New()

	tests := []struct {
		name    string
		scope   auth.Scope
		role    string
		wantErr bool
	}{
		{name: "global satisfies admin", scope: auth.Scope{Kind: auth.ScopeGlobal}, role: "admin"},
		{name: "global satisfies user", scope: auth.Scope{Kind: auth.ScopeGlobal}, role: "user"},
		{name: "global satisfies unknown role", scope: auth.Scope{Kind: auth.ScopeGlobal}, role: "other"},
		{name: "admin satisfies admin", scope: auth.Scope{Kind: auth.ScopeUser, UserID: adminID, Role: "admin"}, role: "admin"},
		{name: "user satisfies user", scope: auth.Scope{Kind: auth.ScopeUser, UserID: adminID, Role: "user"}, role: "user"},
		{name: "user denied admin", scope: auth.Scope{Kind: auth.ScopeUser, UserID: adminID, Role: "user"}, role: "admin", wantErr: true},
		{name: "admin denied user", scope: auth.Scope{Kind: auth.ScopeUser, UserID: adminID, Role: "admin"}, role: "user", wantErr: true},
		{name: "instance never satisfies admin", scope: auth.Scope{Kind: auth.ScopeInstance, InstanceID: instanceID}, role: "admin", wantErr: true},
		{name: "instance never satisfies empty role", scope: auth.Scope{Kind: auth.ScopeInstance, InstanceID: instanceID}, role: "", wantErr: true},
		{name: "empty scope denied", scope: auth.Scope{}, role: "admin", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := auth.RequireRole(tt.scope, tt.role)
			if tt.wantErr && err == nil {
				t.Error("RequireRole = nil, want an error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("RequireRole = %v, want nil", err)
			}
		})
	}
}

func TestRequireInstance(t *testing.T) {
	id := uuid.New()
	other := uuid.New()
	userID := uuid.New()

	tests := []struct {
		name    string
		scope   auth.Scope
		id      uuid.UUID
		wantErr bool
	}{
		{name: "global satisfies any instance", scope: auth.Scope{Kind: auth.ScopeGlobal}, id: id},
		{name: "instance satisfies own id", scope: auth.Scope{Kind: auth.ScopeInstance, InstanceID: id}, id: id},
		{name: "instance denied other id", scope: auth.Scope{Kind: auth.ScopeInstance, InstanceID: id}, id: other, wantErr: true},
		{name: "user always denied", scope: auth.Scope{Kind: auth.ScopeUser, UserID: userID, Role: "admin"}, id: id, wantErr: true},
		{name: "user denied even when ids match", scope: auth.Scope{Kind: auth.ScopeUser, UserID: id, Role: "admin"}, id: id, wantErr: true},
		{name: "empty scope denied", scope: auth.Scope{}, id: id, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := auth.RequireInstance(tt.scope, tt.id)
			if tt.wantErr && err == nil {
				t.Error("RequireInstance = nil, want an error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("RequireInstance = %v, want nil", err)
			}
		})
	}
}
