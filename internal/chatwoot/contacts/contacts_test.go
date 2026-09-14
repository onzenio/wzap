package contacts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"wzap/internal/chatwoot/client"
	"wzap/internal/model"
)

func newTestClient(baseURL string) *client.Client {
	return client.New(baseURL, "test-token", "1")
}

// TestVariantsAddsAndRemovesNinthDigit pins the pure Brazilian variant rule:
// a 13-digit +55 number also matches without the 9th digit, and a 12-digit
// +55 mobile also matches with it.
func TestVariantsAddsAndRemovesNinthDigit(t *testing.T) {
	if got, want := variants("+5511999999999"), []string{"+5511999999999", "+551199999999"}; !reflect.DeepEqual(got, want) {
		t.Errorf("variants(+5511999999999) = %v, want %v", got, want)
	}
	if got, want := variants("+551199999999"), []string{"+551199999999", "+5511999999999"}; !reflect.DeepEqual(got, want) {
		t.Errorf("variants(+551199999999) = %v, want %v", got, want)
	}
}

// TestVariantsKeepsForeignNumbersSingle ensures non-Brazilian numbers and
// Brazilian landlines resolve to a single variant (no alternate form).
func TestVariantsKeepsForeignNumbersSingle(t *testing.T) {
	if got, want := variants("+14155552671"), []string{"+14155552671"}; !reflect.DeepEqual(got, want) {
		t.Errorf("variants(+14155552671) = %v, want %v", got, want)
	}
	if got, want := variants("+552131309999"), []string{"+552131309999"}; !reflect.DeepEqual(got, want) {
		t.Errorf("variants(+552131309999 landline) = %v, want %v", got, want)
	}
}

// TestResolveQueriesVariantsWithoutPlus pins the real lookup contract:
// client-side union — one FindContactByPhone (single-value filter payload
// without "+") per BR variant, 2 calls max per BR lookup, with hits unioned
// and deduped by id. Longest-wins/merge on the union is pinned by
// TestResolveMergesBrazilDuplicates / TestResolveSkipsMergeWhenFlagOff.
// Union (not a single raw OR payload) because the client takes a single
// phone per call.
func TestResolveQueriesVariantsWithoutPlus(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/accounts/1/contacts/filter" {
			_, _ = w.Write([]byte(`{"id":7,"phone_number":"+5511999999999"}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode filter body: %v", err)
		}
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"payload":[]}`))
	}))
	defer srv.Close()

	resolver := New(newTestClient(srv.URL), model.ChatwootConfig{}, nil)
	_, err := resolver.Resolve(context.Background(), "+5511999999999", false, "Nine", "", "5511999999999@s.whatsapp.net")
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("filter calls = %d, want 2 (one per BR variant)", len(bodies))
	}
	seen := map[string]bool{}
	for _, body := range bodies {
		payload, _ := body["payload"].([]any)
		if len(payload) != 1 {
			t.Fatalf("filter payload = %v, want single phone_number condition", body["payload"])
		}
		cond, _ := payload[0].(map[string]any)
		values, _ := cond["values"].([]any)
		if len(values) != 1 {
			t.Fatalf("filter values = %v, want single value", cond["values"])
		}
		value, _ := values[0].(string)
		if strings.Contains(value, "+") {
			t.Errorf("filter value = %q, want no plus sign", value)
		}
		seen[value] = true
	}
	if !seen["5511999999999"] || !seen["551199999999"] {
		t.Errorf("filter values = %v, want both BR variants without plus", seen)
	}
}

// TestResolveMergesBrazilDuplicates ensures multiple BR hits collapse into
// the longest-phone contact via contact_merge when merge_brazil_contacts is on.
// The union behind it (one filter call per variant, deduped by id) is pinned
// here by asserting both variant calls happened despite a single merged result.
func TestResolveMergesBrazilDuplicates(t *testing.T) {
	var gotMerge map[string]any
	filterCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/accounts/1/contacts/filter":
			filterCalls++
			_, _ = w.Write([]byte(`{"payload":[{"id":3,"phone_number":"+551199999999"},{"id":5,"phone_number":"+5511999999999"}]}`))
		case "/api/v1/accounts/1/actions/contact_merge":
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &gotMerge)
			_, _ = w.Write([]byte(`{"id":5,"phone_number":"+5511999999999"}`))
		default:
			t.Errorf("unexpected path %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	resolver := New(newTestClient(srv.URL), model.ChatwootConfig{MergeBrazilContacts: true}, nil)
	got, err := resolver.Resolve(context.Background(), "+5511999999999", false, "", "", "")
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	if got.ID != 5 {
		t.Errorf("contact id = %d, want 5 (longest phone wins)", got.ID)
	}
	if gotMerge["base_contact_id"] != float64(5) || gotMerge["mergee_contact_id"] != float64(3) {
		t.Errorf("merge request = %v, want base 5 mergee 3", gotMerge)
	}
	if filterCalls != 2 {
		t.Errorf("filter calls = %d, want 2 (one per BR variant, unioned)", filterCalls)
	}
}

// TestResolveSkipsMergeWhenFlagOff ensures duplicates resolve to the longest
// phone without merging when merge_brazil_contacts is off.
func TestResolveSkipsMergeWhenFlagOff(t *testing.T) {
	mergeCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/actions/contact_merge") {
			mergeCalls++
		}
		_, _ = w.Write([]byte(`{"payload":[{"id":3,"phone_number":"+551199999999"},{"id":5,"phone_number":"+5511999999999"}]}`))
	}))
	defer srv.Close()

	resolver := New(newTestClient(srv.URL), model.ChatwootConfig{}, nil)
	got, err := resolver.Resolve(context.Background(), "+5511999999999", false, "", "", "")
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	if got.ID != 5 {
		t.Errorf("contact id = %d, want 5 (longest phone wins)", got.ID)
	}
	if mergeCalls != 0 {
		t.Errorf("merge calls = %d, want 0 with flag off", mergeCalls)
	}
}

// TestResolveGroupCreatesWithIdentifier ensures groups look up by identifier
// and are created with the jid identifier plus a (GROUP) name suffix.
func TestResolveGroupCreatesWithIdentifier(t *testing.T) {
	var gotSearch, gotCreate map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/contacts/search"):
			gotSearch = map[string]any{"q": r.URL.Query().Get("q")}
			_, _ = w.Write([]byte(`{"payload":[]}`))
		case r.URL.Path == "/api/v1/accounts/1/contacts" && r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &gotCreate)
			_, _ = w.Write([]byte(`{"id":9,"identifier":"120363@test@g.us","name":"Team (GROUP)"}`))
		default:
			t.Errorf("unexpected path %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	resolver := New(newTestClient(srv.URL), model.ChatwootConfig{}, nil)
	got, err := resolver.Resolve(context.Background(), "", true, "Team", "", "120363@test@g.us")
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	if gotSearch["q"] != "120363@test@g.us" {
		t.Errorf("search q = %v, want group jid", gotSearch["q"])
	}
	if gotCreate["identifier"] != "120363@test@g.us" {
		t.Errorf("create identifier = %v, want group jid", gotCreate["identifier"])
	}
	if gotCreate["name"] != "Team (GROUP)" {
		t.Errorf("create name = %v, want Team (GROUP)", gotCreate["name"])
	}
	if got.ID != 9 {
		t.Errorf("contact id = %d, want 9", got.ID)
	}
}

// TestResolveUpdatesDivergentName ensures an existing contact gains the new
// name/avatar when stored values are empty or divergent.
func TestResolveUpdatesDivergentName(t *testing.T) {
	var gotUpdate map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/contacts/filter"):
			_, _ = w.Write([]byte(`{"payload":[{"id":4,"name":"Old","phone_number":"+14155552671"}]}`))
		case strings.HasSuffix(r.URL.Path, "/contacts/4") && r.Method == http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &gotUpdate)
			_, _ = w.Write([]byte(`{"id":4,"name":"New","phone_number":"+14155552671","avatar_url":"http://img/a.png"}`))
		default:
			t.Errorf("unexpected path %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	resolver := New(newTestClient(srv.URL), model.ChatwootConfig{}, nil)
	got, err := resolver.Resolve(context.Background(), "+14155552671", false, "New", "http://img/a.png", "")
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	if gotUpdate["name"] != "New" || gotUpdate["avatar_url"] != "http://img/a.png" {
		t.Errorf("update request = %v, want new name and avatar", gotUpdate)
	}
	if got.Name != "New" {
		t.Errorf("contact name = %q, want New", got.Name)
	}
}

// TestResolveCreateConflictFallsBackToIdentifier ensures a 422 on create with
// a jid hint recovers through an identifier search instead of failing.
func TestResolveCreateConflictFallsBackToIdentifier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/contacts/filter"):
			_, _ = w.Write([]byte(`{"payload":[]}`))
		case r.URL.Path == "/api/v1/accounts/1/contacts" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"phone taken"}`))
		case strings.HasSuffix(r.URL.Path, "/contacts/search"):
			if r.URL.Query().Get("q") != "5511999999999@s.whatsapp.net" {
				t.Errorf("search q = %q, want sender jid", r.URL.Query().Get("q"))
			}
			_, _ = w.Write([]byte(`{"payload":[{"id":11,"phone_number":"+5511999999999"}]}`))
		case strings.HasSuffix(r.URL.Path, "/contacts/11") && r.Method == http.MethodPut:
			_, _ = w.Write([]byte(`{"id":11,"name":"Nine","phone_number":"+5511999999999"}`))
		default:
			t.Errorf("unexpected path %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	resolver := New(newTestClient(srv.URL), model.ChatwootConfig{}, nil)
	got, err := resolver.Resolve(context.Background(), "+5511999999999", false, "Nine", "", "5511999999999@s.whatsapp.net")
	if err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
	if got.ID != 11 {
		t.Errorf("contact id = %d, want 11 (recovered by identifier)", got.ID)
	}
}

// TestResolveCreateFailureReturnsNil ensures a creation failure returns nil
// (with the error) so the caller skips the message without killing the worker.
func TestResolveCreateFailureReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/contacts/filter") {
			_, _ = w.Write([]byte(`{"payload":[]}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	}))
	defer srv.Close()

	resolver := New(newTestClient(srv.URL), model.ChatwootConfig{}, nil)
	got, err := resolver.Resolve(context.Background(), "+14155552671", false, "Name", "", "")
	if err == nil {
		t.Fatal("Resolve error = nil, want failure")
	}
	if got != nil {
		t.Errorf("contact = %+v, want nil on creation failure", got)
	}
}
