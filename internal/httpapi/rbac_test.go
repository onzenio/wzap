package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/auth"
	"wzap/internal/config"
	"wzap/internal/instance"
	"wzap/internal/media"
	"wzap/internal/message"
	"wzap/internal/model"
)

// rbacFixture carries the identities and instances a scope test needs: two
// users owning one instance each, one legacy instance without owner, and the
// credentials resolving to each scope.
type rbacFixture struct {
	userA      uuid.UUID
	userB      uuid.UUID
	admin      uuid.UUID
	instA      *model.Instance
	instB      *model.Instance
	legacy     *model.Instance
	adminTok   string
	userATok   string
	userBTok   string
	keyA       string
	keyB       string
	keys       *fakeAPIKeyRepository
	globalKey  string
	messageID  uuid.UUID
	mediaID    uuid.UUID
	mediaForB  *model.Media
	mediaBytes []byte
}

func newRBACFixture(t *testing.T) *rbacFixture {
	t.Helper()
	f := &rbacFixture{
		userA:     uuid.New(),
		userB:     uuid.New(),
		admin:     uuid.New(),
		globalKey: testToken,
		keyA:      "rbac-instance-key-a",
		keyB:      "rbac-instance-key-b",
		messageID: uuid.New(),
		mediaID:   uuid.New(),
	}
	userA := f.userA
	userB := f.userB
	f.instA = &model.Instance{ID: uuid.New(), Name: "a", Status: "disconnected", OwnerUserID: &userA}
	f.instB = &model.Instance{ID: uuid.New(), Name: "b", Status: "disconnected", OwnerUserID: &userB}
	f.legacy = &model.Instance{ID: uuid.New(), Name: "legacy", Status: "disconnected"}
	f.mediaBytes = []byte("bytes da midia")
	f.mediaForB = &model.Media{
		ID:         f.mediaID,
		InstanceID: f.instB.ID,
		Mimetype:   "image/jpeg",
		Filename:   "foto.jpg",
		SizeBytes:  int64(len(f.mediaBytes)),
	}

	var err error
	if f.adminTok, err = auth.MintToken(f.admin, "admin", testJWTSecret); err != nil {
		t.Fatalf("MintToken admin: %v", err)
	}
	if f.userATok, err = auth.MintToken(f.userA, "user", testJWTSecret); err != nil {
		t.Fatalf("MintToken userA: %v", err)
	}
	if f.userBTok, err = auth.MintToken(f.userB, "user", testJWTSecret); err != nil {
		t.Fatalf("MintToken userB: %v", err)
	}

	hash := func(key string) string {
		sum := sha256.Sum256([]byte(key))
		return hex.EncodeToString(sum[:])
	}
	f.keys = &fakeAPIKeyRepository{byHash: map[string]uuid.UUID{
		hash(f.keyA): f.instA.ID,
		hash(f.keyB): f.instB.ID,
	}}
	return f
}

func (f *rbacFixture) instancesByID() map[uuid.UUID]*model.Instance {
	return map[uuid.UUID]*model.Instance{
		f.instA.ID:  f.instA,
		f.instB.ID:  f.instB,
		f.legacy.ID: f.legacy,
	}
}

// rbacInstances returns an InstanceService answering Get from the fixture and
// List with all three rows; mutating calls succeed.
func (f *rbacFixture) rbacInstances() *fakeInstanceService {
	byID := f.instancesByID()
	return &fakeInstanceService{
		getFn: func(_ context.Context, id uuid.UUID) (*model.Instance, error) {
			if inst, ok := byID[id]; ok {
				return inst, nil
			}
			return nil, instance.ErrNotFound
		},
		listFn: func(context.Context, int, string) ([]model.Instance, string, error) {
			return []model.Instance{*f.instA, *f.instB, *f.legacy}, "", nil
		},
	}
}

// rbacMessages returns a MessageService answering every read with one row.
func (f *rbacFixture) rbacMessages() *fakeMessageService {
	return &fakeMessageService{
		getFn: func(_ context.Context, instanceID, messageID uuid.UUID) (*model.OutboundMessage, error) {
			return &model.OutboundMessage{
				ID: messageID, InstanceID: instanceID,
				Type: message.TypeText, Status: "sent",
			}, nil
		},
		listFn: func(_ context.Context, instanceID uuid.UUID, _ int, _ string) ([]model.OutboundMessage, string, error) {
			return []model.OutboundMessage{{
				ID: f.messageID, InstanceID: instanceID,
				Type: message.TypeText, Status: "sent",
			}}, "", nil
		},
	}
}

// rbacMedia returns a MediaStore answering Open for the fixture media and
// accepting uploads.
func (f *rbacFixture) rbacMedia() *fakeMediaStore {
	return &fakeMediaStore{
		openFn: func(_ context.Context, id uuid.UUID) (io.ReadCloser, *model.Media, error) {
			if id == f.mediaID {
				return io.NopCloser(bytes.NewReader(f.mediaBytes)), f.mediaForB, nil
			}
			return nil, nil, media.ErrNotFound
		},
	}
}

// rbacServer wires every fixture service behind Authenticate.
func (f *rbacFixture) rbacServer(t *testing.T) *http.Server {
	t.Helper()
	return New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: f.globalKey, JWTSecret: testJWTSecret, MaxMediaBytes: testMaxMediaBytes},
		discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Instances:    f.rbacInstances(),
			Messages:     f.rbacMessages(),
			Media:        f.rbacMedia(),
			Numbers:      &fakeNumberResolver{resolveFn: func(context.Context, uuid.UUID, string) (string, error) { return "5547988359190@s.whatsapp.net", nil }},
			Idempotency:  newFakeIdempotency(),
			Users:        newFakeUserRepository(),
			Keys:         f.keys,
			JWTSecret:    testJWTSecret,
		},
	)
}

func rbacSessionCookie(token string) *http.Cookie {
	return &http.Cookie{Name: auth.SessionCookieName, Value: token}
}

// serveRBAC sends a request carrying exactly one credential: a session cookie
// or an apikey header value.
func serveRBAC(t *testing.T, srv *http.Server, method, path, body string, cookie *http.Cookie, apiKey string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if apiKey != "" {
		req.Header.Set("apikey", apiKey)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

// instanceRouteCases lists one representative request per instance route
// class, built against target. The caller supplies the credential; every case
// uses a valid body so a denial proves gating, not validation.
func instanceRouteCases(target uuid.UUID, mediaID uuid.UUID) []struct {
	name   string
	method string
	path   string
	body   string
} {
	msgID := uuid.NewString()
	return []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "get one", method: http.MethodGet, path: "/instances/" + target.String()},
		{name: "update one", method: http.MethodPatch, path: "/instances/" + target.String(), body: `{"name":"novo"}`},
		{name: "delete one", method: http.MethodDelete, path: "/instances/" + target.String()},
		{name: "connect", method: http.MethodPost, path: "/instances/" + target.String() + "/connect"},
		{name: "qr", method: http.MethodGet, path: "/instances/" + target.String() + "/qr"},
		{name: "status", method: http.MethodGet, path: "/instances/" + target.String() + "/status"},
		{name: "disconnect", method: http.MethodPost, path: "/instances/" + target.String() + "/disconnect"},
		{name: "numbers check", method: http.MethodPost, path: "/instances/" + target.String() + "/numbers/check", body: `{"phone":"5547988359190"}`},
		{name: "send text", method: http.MethodPost, path: "/instances/" + target.String() + "/messages/text", body: `{"to":"5547988359190","text":"ola"}`},
		{name: "send location", method: http.MethodPost, path: "/instances/" + target.String() + "/messages/location", body: `{"to":"5547988359190","latitude":-23.55,"longitude":-46.63}`},
		{name: "send contact", method: http.MethodPost, path: "/instances/" + target.String() + "/messages/contact", body: `{"to":"5547988359190","display_name":"Fulano","vcard":"BEGIN:VCARD"}`},
		{name: "get message", method: http.MethodGet, path: "/instances/" + target.String() + "/messages/" + msgID},
		{name: "list messages", method: http.MethodGet, path: "/instances/" + target.String() + "/messages"},
		{name: "download media of instance", method: http.MethodGet, path: "/media/" + mediaID.String()},
	}
}

func TestRBACUserCannotReachOthersInstance(t *testing.T) {
	f := newRBACFixture(t)
	srv := f.rbacServer(t)
	userA := rbacSessionCookie(f.userATok)

	for _, tc := range instanceRouteCases(f.instB.ID, f.mediaID) {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveRBAC(t, srv, tc.method, tc.path, tc.body, userA, "", nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: status = %d, want %d (body %q)", tc.name, rec.Code, http.StatusForbidden, rec.Body.String())
			}
			if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
				t.Errorf("%s: error code = %q, want %q", tc.name, code, "forbidden")
			}
		})
	}
}

func TestRBACInstanceKeyCannotReachOtherInstance(t *testing.T) {
	f := newRBACFixture(t)
	srv := f.rbacServer(t)

	// The download case addresses the media of instB while the other cases
	// address instB directly; all must deny keyA.
	for _, tc := range instanceRouteCases(f.instB.ID, f.mediaID) {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveRBAC(t, srv, tc.method, tc.path, tc.body, nil, f.keyA, nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: status = %d, want %d (body %q)", tc.name, rec.Code, http.StatusForbidden, rec.Body.String())
			}
			if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
				t.Errorf("%s: error code = %q, want %q", tc.name, code, "forbidden")
			}
		})
	}
}

func TestRBACInstanceKeyDeniedCollections(t *testing.T) {
	f := newRBACFixture(t)
	srv := f.rbacServer(t)

	t.Run("list all", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodGet, "/instances", "", nil, f.keyA, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
			t.Errorf("error code = %q, want %q", code, "forbidden")
		}
	})

	t.Run("create", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, nil, f.keyA, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
			t.Errorf("error code = %q, want %q", code, "forbidden")
		}
	})
}

func TestRBACRandomUUIDIsNotFound(t *testing.T) {
	f := newRBACFixture(t)
	unknown := uuid.New()
	unknownMedia := uuid.New()
	srv := New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: f.globalKey, JWTSecret: testJWTSecret, MaxMediaBytes: testMaxMediaBytes},
		discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Instances: &fakeInstanceService{
				getFn: func(context.Context, uuid.UUID) (*model.Instance, error) { return nil, instance.ErrNotFound },
				listFn: func(context.Context, int, string) ([]model.Instance, string, error) {
					return nil, "", nil
				},
			},
			Messages:    f.rbacMessages(),
			Media:       &fakeMediaStore{},
			Numbers:     &fakeNumberResolver{},
			Idempotency: newFakeIdempotency(),
			Keys:        f.keys,
			JWTSecret:   testJWTSecret,
		},
	)
	userA := rbacSessionCookie(f.userATok)

	// Every instance route class shares the unknown instance id; media uses
	// an unknown media id. Any scope asking for them gets 404 before any
	// ownership check.
	cases := instanceRouteCases(unknown, unknownMedia)
	creds := []struct {
		name   string
		cookie *http.Cookie
		apiKey string
	}{
		{name: "user", cookie: userA},
		{name: "instance key", apiKey: f.keyA},
		{name: "global", apiKey: f.globalKey},
	}
	for _, cred := range creds {
		t.Run(cred.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					rec := serveRBAC(t, srv, tc.method, tc.path, tc.body, cred.cookie, cred.apiKey, nil)
					if rec.Code != http.StatusNotFound {
						t.Fatalf("%s: status = %d, want %d (body %q)", tc.name, rec.Code, http.StatusNotFound, rec.Body.String())
					}
					if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
						t.Errorf("%s: error code = %q, want %q", tc.name, code, "not_found")
					}
				})
			}

			t.Run("upload media to unknown instance", func(t *testing.T) {
				body, formType := multipartBody(t,
					[][2]string{{"to", "5547988359190"}, {"type", "image"}},
					"foto.jpg", "image/jpeg", []byte("bytes"))
				req := httptest.NewRequest(http.MethodPost,
					"/instances/"+unknown.String()+"/messages/media", bytes.NewReader(body))
				req.Header.Set("Content-Type", formType)
				if cred.cookie != nil {
					req.AddCookie(cred.cookie)
				}
				if cred.apiKey != "" {
					req.Header.Set("apikey", cred.apiKey)
				}
				rec := httptest.NewRecorder()
				srv.Handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusNotFound, rec.Body.String())
				}
				if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
					t.Errorf("error code = %q, want %q", code, "not_found")
				}
			})
		})
	}
}

func TestRBACListFiltersByOwner(t *testing.T) {
	f := newRBACFixture(t)
	srv := f.rbacServer(t)

	decodeIDs := func(t *testing.T, body []byte) []string {
		t.Helper()
		var payload struct {
			Data instanceListResponse `json:"data"`
		}
		decodeJSON(t, body, &payload)
		ids := make([]string, 0, len(payload.Data.Items))
		for _, item := range payload.Data.Items {
			ids = append(ids, item.ID)
		}
		return ids
	}
	contains := func(ids []string, want uuid.UUID) bool {
		for _, id := range ids {
			if id == want.String() {
				return true
			}
		}
		return false
	}

	t.Run("user sees own only", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodGet, "/instances", "", rbacSessionCookie(f.userATok), "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
		ids := decodeIDs(t, rec.Body.Bytes())
		if !contains(ids, f.instA.ID) {
			t.Errorf("list = %v, want it to contain the own instance %s", ids, f.instA.ID)
		}
		if contains(ids, f.instB.ID) {
			t.Errorf("list = %v, want it to exclude the other owner's instance %s", ids, f.instB.ID)
		}
		if contains(ids, f.legacy.ID) {
			t.Errorf("list = %v, want it to exclude the legacy NULL-owner instance %s", ids, f.legacy.ID)
		}
	})

	t.Run("admin sees all", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodGet, "/instances", "", rbacSessionCookie(f.adminTok), "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
		ids := decodeIDs(t, rec.Body.Bytes())
		for _, want := range []uuid.UUID{f.instA.ID, f.instB.ID, f.legacy.ID} {
			if !contains(ids, want) {
				t.Errorf("list = %v, want it to contain %s", ids, want)
			}
		}
	})

	t.Run("global sees all", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodGet, "/instances", "", nil, f.globalKey, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
		ids := decodeIDs(t, rec.Body.Bytes())
		for _, want := range []uuid.UUID{f.instA.ID, f.instB.ID, f.legacy.ID} {
			if !contains(ids, want) {
				t.Errorf("list = %v, want it to contain %s", ids, want)
			}
		}
	})
}

func TestRBACAdminAndOwnerReachInstance(t *testing.T) {
	f := newRBACFixture(t)
	srv := f.rbacServer(t)

	t.Run("owner reaches own", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodGet, "/instances/"+f.instA.ID.String(), "", rbacSessionCookie(f.userATok), "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
	})

	t.Run("admin reaches other's", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodGet, "/instances/"+f.instB.ID.String(), "", rbacSessionCookie(f.adminTok), "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
	})

	t.Run("instance key reaches own", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodGet, "/instances/"+f.instA.ID.String(), "", nil, f.keyA, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
	})

	t.Run("legacy visible to admin only", func(t *testing.T) {
		if rec := serveRBAC(t, srv, http.MethodGet, "/instances/"+f.legacy.ID.String(), "", rbacSessionCookie(f.userATok), "", nil); rec.Code != http.StatusForbidden {
			t.Errorf("user on legacy: status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		if rec := serveRBAC(t, srv, http.MethodGet, "/instances/"+f.legacy.ID.String(), "", rbacSessionCookie(f.adminTok), "", nil); rec.Code != http.StatusOK {
			t.Errorf("admin on legacy: status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
		if rec := serveRBAC(t, srv, http.MethodGet, "/instances/"+f.legacy.ID.String(), "", nil, f.globalKey, nil); rec.Code != http.StatusOK {
			t.Errorf("global on legacy: status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
	})
}

func TestRBACMediaDownload(t *testing.T) {
	f := newRBACFixture(t)
	srv := f.rbacServer(t)
	path := "/media/" + f.mediaID.String()

	t.Run("owner of other instance is forbidden", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodGet, path, "", rbacSessionCookie(f.userATok), "", nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})

	t.Run("other instance key is forbidden", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodGet, path, "", nil, f.keyA, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})

	t.Run("owner reaches own media", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodGet, path, "", rbacSessionCookie(f.userBTok), "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
		if !bytes.Equal(rec.Body.Bytes(), f.mediaBytes) {
			t.Errorf("body = %q, want %q", rec.Body.Bytes(), f.mediaBytes)
		}
	})

	t.Run("missing media is not found", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodGet, "/media/"+uuid.NewString(), "", rbacSessionCookie(f.userATok), "", nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusNotFound, rec.Body.String())
		}
	})
}

func TestRBACMediaUploadGating(t *testing.T) {
	f := newRBACFixture(t)
	srv := f.rbacServer(t)
	fields := [][2]string{{"to", "5547988359190"}, {"type", "image"}}

	upload := func(t *testing.T, target uuid.UUID, cookie *http.Cookie, apiKey string) *httptest.ResponseRecorder {
		t.Helper()
		body, formType := multipartBody(t, fields, "foto.jpg", "image/jpeg", []byte("bytes"))
		req := httptest.NewRequest(http.MethodPost, "/instances/"+target.String()+"/messages/media", bytes.NewReader(body))
		req.Header.Set("Content-Type", formType)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if apiKey != "" {
			req.Header.Set("apikey", apiKey)
		}
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		return rec
	}

	t.Run("user on other's instance is forbidden", func(t *testing.T) {
		if rec := upload(t, f.instB.ID, rbacSessionCookie(f.userATok), ""); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})

	t.Run("other key is forbidden", func(t *testing.T) {
		if rec := upload(t, f.instB.ID, nil, f.keyA); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})

	t.Run("owner upload accepted", func(t *testing.T) {
		if rec := upload(t, f.instB.ID, rbacSessionCookie(f.userBTok), ""); rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusAccepted, rec.Body.String())
		}
	})
}

func TestRBACCreateGating(t *testing.T) {
	f := newRBACFixture(t)
	created := &model.Instance{ID: uuid.New(), Name: "loja", Status: "disconnected"}
	svc := &fakeInstanceService{
		getFn: func(_ context.Context, id uuid.UUID) (*model.Instance, error) {
			if id == created.ID {
				return created, nil
			}
			return nil, instance.ErrNotFound
		},
		createFn: func(_ context.Context, input instance.CreateInput) (*model.Instance, string, error) {
			return created, "one-time-key", nil
		},
	}
	srv := New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: f.globalKey, JWTSecret: testJWTSecret},
		discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Instances:    svc,
			Keys:         f.keys,
			Users:        newFakeUserRepository(),
			JWTSecret:    testJWTSecret,
		},
	)

	t.Run("user may create", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, rbacSessionCookie(f.userATok), "", nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
		}
	})

	t.Run("admin may create", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, rbacSessionCookie(f.adminTok), "", nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
		}
	})

	t.Run("instance key may not create", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, nil, f.keyA, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})
}
