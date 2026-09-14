package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"wzap/internal/auth"
	"wzap/internal/model"
	"wzap/internal/storage"
)

// loginRequest is the POST /auth/login payload.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// identityResponse is the JSON representation of the authenticated manager
// user, shared by login and me.
type identityResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// logoutResponse is the POST /auth/logout payload.
type logoutResponse struct {
	Status string `json:"status"`
}

// handleLogin verifies the credentials and answers 200 with the identity plus
// the session cookie. An unknown email and a wrong password share one 401
// response so neither field is revealed.
func handleLogin(users storage.UserRepository, jwtSecret string, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request loginRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			writeJSONBodyError(w, r, err)
			return
		}

		user, err := users.GetByEmail(r.Context(), request.Email)
		switch {
		case errors.Is(err, storage.ErrNotFound):
			Error(w, r, http.StatusUnauthorized, "unauthorized", "invalid credentials")
			return
		case err != nil:
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}

		if err := auth.CheckPassword(user.PasswordHash, request.Password); err != nil {
			Error(w, r, http.StatusUnauthorized, "unauthorized", "invalid credentials")
			return
		}

		token, err := auth.MintToken(user.ID, user.Role, jwtSecret)
		if err != nil {
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		setSessionCookie(w, token, secure)
		JSON(w, http.StatusOK, newIdentityResponse(user))
	}
}

// handleLogout clears the session cookie and answers 200. There are no
// server-side sessions, so revocation is the cleared cookie alone.
func handleLogout(secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		clearSessionCookie(w, secure)
		JSON(w, http.StatusOK, logoutResponse{Status: "ok"})
	}
}

// handleMe answers 200 with the identity of the session cookie holder, 401
// without a valid session.
func handleMe(users storage.UserRepository, jwtSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.SessionCookieName)
		if err != nil {
			Error(w, r, http.StatusUnauthorized, "unauthorized", "missing or invalid session")
			return
		}
		scope, err := auth.ParseToken(cookie.Value, jwtSecret)
		if err != nil {
			Error(w, r, http.StatusUnauthorized, "unauthorized", "missing or invalid session")
			return
		}

		user, err := users.GetByID(r.Context(), scope.UserID)
		switch {
		case errors.Is(err, storage.ErrNotFound):
			Error(w, r, http.StatusUnauthorized, "unauthorized", "missing or invalid session")
			return
		case err != nil:
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		JSON(w, http.StatusOK, newIdentityResponse(user))
	}
}

// newIdentityResponse maps a stored user to its JSON representation. The
// password hash never leaves the storage boundary.
func newIdentityResponse(user *model.User) identityResponse {
	return identityResponse{ID: user.ID.String(), Email: user.Email, Role: user.Role}
}

// setSessionCookie stores the session token with the attributes the session
// contract requires: Path /, HttpOnly, SameSite Lax, a MaxAge matching the
// token lifetime, and Secure exactly when the public URL serves https.
func setSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(auth.TokenLifetime.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie expires the session cookie with the same attributes, so
// the client drops the session it holds.
func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// secureCookies reports whether session cookies must carry the Secure flag:
// exactly when PublicURL serves https.
func secureCookies(publicURL string) bool {
	return strings.HasPrefix(strings.ToLower(publicURL), "https://")
}
