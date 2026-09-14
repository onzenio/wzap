package httpapi

import (
	"net/http"

	"wzap/internal/auth"
	"wzap/internal/model"
)

// authorizeInstance is the single ownership gate every instance-scoped handler
// calls: the target is already loaded (missing → 404 before this runs) and a
// denial answers 403. The global scope and admin sessions reach every
// instance; a user session reaches exactly the instances it owns (legacy
// NULL-owner rows stay visible to global/admin only); an instance key reaches
// exactly its own instance.
func authorizeInstance(r *http.Request, inst *model.Instance) error {
	scope, ok := auth.ScopeFromContext(r.Context())
	if !ok {
		return auth.ErrForbidden
	}
	if err := auth.RequireRole(scope, "admin"); err == nil {
		return nil
	}
	if scope.Kind == auth.ScopeUser {
		if inst.OwnerUserID != nil && *inst.OwnerUserID == scope.UserID {
			return nil
		}
		return auth.ErrForbidden
	}
	if err := auth.RequireInstance(scope, inst.ID); err == nil {
		return nil
	}
	return auth.ErrForbidden
}

// authorizeCollection gates the collection/general routes (list and create
// instances): the global scope and any user session may proceed, while an
// instance key authorizes nothing outside its own instance. A denial answers
// 403.
func authorizeCollection(r *http.Request) error {
	scope, ok := auth.ScopeFromContext(r.Context())
	if !ok {
		return auth.ErrForbidden
	}
	switch scope.Kind {
	case auth.ScopeGlobal, auth.ScopeUser:
		return nil
	default:
		return auth.ErrForbidden
	}
}

// filterInstancesByOwner narrows a listed page to the instances the scope may
// see: the global scope and admin sessions keep every row, user sessions keep
// exactly their own (legacy NULL-owner rows drop out for them).
func filterInstancesByOwner(scope auth.Scope, items []model.Instance) []model.Instance {
	if err := auth.RequireRole(scope, "admin"); err == nil {
		return items
	}
	filtered := make([]model.Instance, 0, len(items))
	for _, item := range items {
		if item.OwnerUserID != nil && *item.OwnerUserID == scope.UserID {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// writeForbidden answers the shared 403 envelope for a scope denial.
func writeForbidden(w http.ResponseWriter, r *http.Request) {
	Error(w, r, http.StatusForbidden, "forbidden", "forbidden")
}
