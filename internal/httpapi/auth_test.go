package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"wzap/internal/auth"
	"wzap/internal/config"
	"wzap/internal/model"
	"wzap/internal/storage"
)

// fakeUserRepository is an in-memory storage.UserRepository keyed by id and
// email.
type fakeUserRepository struct {
	byID    map[uuid.UUID]*model.User
	byEmail map[string]*model.User
	err     error
}

func newFakeUserRepository(users ...*model.User) *fakeUserRepository {
	f := &fakeUserRepository{byID: map[uuid.UUID]*model.User{}, byEmail: map[string]*model.User{}}
	for _, u := range users {
		f.byID[u.ID] = u
		f.byEmail[strings.ToLower(u.Email)] = u
	}
	return f
}

func (f *fakeUserRepository) Create(_ context.Context, user model.User) (*model.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.byID[user.ID] = &user
	f.byEmail[strings.ToLower(user.Email)] = &user
	return &user, nil
}

func (f *fakeUserRepository) GetByID(_ context.Context, id uuid.UUID) (*model.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, storage.ErrNotFound
}

func (f *fakeUserRepository) GetByEmail(_ context.Context, email string) (*model.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	if u, ok := f.byEmail[strings.ToLower(email)]; ok {
		return u, nil
	}
	return nil, storage.ErrNotFound
}

func (f *fakeUserRepository) List(context.Context) ([]model.User, error) {
	return nil, nil
}

func (f *fakeUserRepository) Delete(_ context.Context, id uuid.UUID) error {
	delete(f.byID, id)
	return nil
}

func (f *fakeUserRepository) Count(context.Context) (int, error) { return len(f.byID), nil }

const testJWTSecret = "test-jwt-secret-that-is-long-enough"

func authTestServer(t *testing.T, users storage.UserRepository, publicURL string) *http.Server {
	t.Helper()
	return New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken, JWTSecret: testJWTSecret, PublicURL: publicURL},
		discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Users:        users,
			JWTSecret:    testJWTSecret,
		},
	)
}

func seedAuthUser(t *testing.T, email, password, role string) *model.User {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	return &model.User{ID: uuid.New(), Email: email, PasswordHash: hash, Role: role}
}

// serveAuth sends a request without the service token: the session endpoints
// authenticate with the cookie, never with the Bearer token.
func serveAuth(t *testing.T, srv *http.Server, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	t.Fatalf("response sets no %q cookie (Set-Cookie: %q)", auth.SessionCookieName, rec.Header().Values("Set-Cookie"))
	return nil
}

func TestAuthLoginSuccess(t *testing.T) {
	user := seedAuthUser(t, "admin@example.com", "s3cret-password", "admin")
	srv := authTestServer(t, newFakeUserRepository(user), "http://localhost:8080")

	rec := serveAuth(t, srv, http.MethodPost, "/api/v1/auth/login",
		`{"email":"admin@example.com","password":"s3cret-password"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Data struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if payload.Data.ID != user.ID.String() {
		t.Errorf("data.id = %q, want %q", payload.Data.ID, user.ID)
	}
	if payload.Data.Email != user.Email {
		t.Errorf("data.email = %q, want %q", payload.Data.Email, user.Email)
	}
	if payload.Data.Role != "admin" {
		t.Errorf("data.role = %q, want %q", payload.Data.Role, "admin")
	}

	cookie := sessionCookie(t, rec)
	if cookie.Value == "" {
		t.Error("session cookie value is empty")
	}
	if cookie.Path != "/" {
		t.Errorf("cookie path = %q, want %q", cookie.Path, "/")
	}
	if !cookie.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.MaxAge != int((12 * time.Hour).Seconds()) {
		t.Errorf("cookie MaxAge = %d, want %d", cookie.MaxAge, int((12 * time.Hour).Seconds()))
	}
	if cookie.Secure {
		t.Error("cookie is Secure over plain http, want Secure only for https")
	}

	if _, err := auth.ParseToken(cookie.Value, testJWTSecret); err != nil {
		t.Errorf("session cookie is not a valid token: %v", err)
	}
}

func TestAuthLoginSetsSecureCookieForHTTPS(t *testing.T) {
	user := seedAuthUser(t, "admin@example.com", "s3cret-password", "admin")
	srv := authTestServer(t, newFakeUserRepository(user), "https://wzap.example.com")

	rec := serveAuth(t, srv, http.MethodPost, "/api/v1/auth/login",
		`{"email":"admin@example.com","password":"s3cret-password"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if cookie := sessionCookie(t, rec); !cookie.Secure {
		t.Error("cookie is not Secure over https, want Secure")
	}
}

func TestAuthLoginRejectsInvalidCredentials(t *testing.T) {
	user := seedAuthUser(t, "admin@example.com", "s3cret-password", "admin")
	srv := authTestServer(t, newFakeUserRepository(user), "")

	unknown := serveAuth(t, srv, http.MethodPost, "/api/v1/auth/login",
		`{"email":"nobody@example.com","password":"s3cret-password"}`)
	wrong := serveAuth(t, srv, http.MethodPost, "/api/v1/auth/login",
		`{"email":"admin@example.com","password":"wrong-password"}`)

	for name, rec := range map[string]*httptest.ResponseRecorder{"unknown email": unknown, "wrong password": wrong} {
		t.Run(name, func(t *testing.T) {
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
			if code := errorCode(t, rec.Body.Bytes()); code == "" {
				t.Error("response has no error code")
			}
		})
	}

	if unknown.Body.String() != wrong.Body.String() {
		t.Errorf("responses differ, want identical bodies:\nunknown: %s\nwrong: %s",
			unknown.Body.String(), wrong.Body.String())
	}
	for _, leak := range []string{"email", "password", "hash"} {
		if strings.Contains(strings.ToLower(unknown.Body.String()), leak) {
			t.Errorf("response indicates the failing field (%q): %q", leak, unknown.Body.String())
		}
	}
	for _, rec := range []*httptest.ResponseRecorder{unknown, wrong} {
		for _, c := range rec.Result().Cookies() {
			if c.Name == auth.SessionCookieName && c.Value != "" {
				t.Errorf("failed login sets a session cookie: %+v", c)
			}
		}
	}
}

func TestAuthLoginRejectsMalformedBody(t *testing.T) {
	srv := authTestServer(t, newFakeUserRepository(), "")

	rec := serveAuth(t, srv, http.MethodPost, "/api/v1/auth/login", `{"email":`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "invalid_request" {
		t.Errorf("error code = %q, want %q", code, "invalid_request")
	}
}

func TestAuthMe(t *testing.T) {
	user := seedAuthUser(t, "me@example.com", "s3cret-password", "user")
	srv := authTestServer(t, newFakeUserRepository(user), "")

	login := serveAuth(t, srv, http.MethodPost, "/api/v1/auth/login",
		`{"email":"me@example.com","password":"s3cret-password"}`)
	cookie := sessionCookie(t, login)

	t.Run("valid session", func(t *testing.T) {
		rec := serveAuth(t, srv, http.MethodGet, "/api/v1/auth/me", "", cookie)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
		var payload struct {
			Data struct {
				ID    string `json:"id"`
				Email string `json:"email"`
				Role  string `json:"role"`
			} `json:"data"`
		}
		decodeJSON(t, rec.Body.Bytes(), &payload)
		if payload.Data.ID != user.ID.String() || payload.Data.Email != user.Email || payload.Data.Role != "user" {
			t.Errorf("data = %+v, want the identity of %s", payload.Data, user.Email)
		}
	})

	t.Run("without session", func(t *testing.T) {
		rec := serveAuth(t, srv, http.MethodGet, "/api/v1/auth/me", "")

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})

	t.Run("tampered token", func(t *testing.T) {
		bad := *cookie
		bad.Value += "x"
		rec := serveAuth(t, srv, http.MethodGet, "/api/v1/auth/me", "", &bad)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})

	t.Run("expired token", func(t *testing.T) {
		expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub": user.ID.String(), "role": user.Role,
			"exp": time.Now().Add(-time.Hour).Unix(),
		}).SignedString([]byte(testJWTSecret))
		if err != nil {
			t.Fatalf("sign expired token: %v", err)
		}
		rec := serveAuth(t, srv, http.MethodGet, "/api/v1/auth/me", "",
			&http.Cookie{Name: auth.SessionCookieName, Value: expired})

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})
}

func TestAuthLogoutInvalidatesSession(t *testing.T) {
	user := seedAuthUser(t, "me@example.com", "s3cret-password", "user")
	srv := authTestServer(t, newFakeUserRepository(user), "")

	login := serveAuth(t, srv, http.MethodPost, "/api/v1/auth/login",
		`{"email":"me@example.com","password":"s3cret-password"}`)
	cookie := sessionCookie(t, login)

	if rec := serveAuth(t, srv, http.MethodGet, "/api/v1/auth/me", "", cookie); rec.Code != http.StatusOK {
		t.Fatalf("me before logout = %d, want %d", rec.Code, http.StatusOK)
	}

	logout := serveAuth(t, srv, http.MethodPost, "/api/v1/auth/logout", "", cookie)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout status = %d, want %d (body %q)", logout.Code, http.StatusOK, logout.Body.String())
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	decodeJSON(t, logout.Body.Bytes(), &payload)
	if payload.Data.Status != "ok" {
		t.Errorf("data.status = %q, want %q", payload.Data.Status, "ok")
	}

	cleared := sessionCookie(t, logout)
	if cleared.Value != "" {
		t.Errorf("logout cookie value = %q, want empty", cleared.Value)
	}
	if cleared.MaxAge > 0 {
		t.Errorf("logout cookie MaxAge = %d, want expired", cleared.MaxAge)
	}

	// The client jar holds no usable session afterwards: /auth/me without the
	// cookie is unauthorized and re-adds no session.
	after := serveAuth(t, srv, http.MethodGet, "/api/v1/auth/me", "")
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d, want %d (body %q)", after.Code, http.StatusUnauthorized, after.Body.String())
	}
	for _, c := range after.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			t.Errorf("me after logout re-adds a session cookie: %+v", c)
		}
	}
}
