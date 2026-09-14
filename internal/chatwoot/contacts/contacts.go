// Package contacts resolves WhatsApp senders to Chatwoot contacts, applying
// the Brazilian 9th-digit rule: a +55 number is looked up with and without
// the extra 9, duplicates merge when configured, and groups resolve by their
// own JID identifier.
package contacts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"wzap/internal/chatwoot/client"
	"wzap/internal/model"
)

// groupSuffix marks group contact names so they never collide with people.
const groupSuffix = " (GROUP)"

// Contact is the minimal resolved Chatwoot contact projection the mirror
// worker needs: the Chatwoot id plus the phone/identifier correlation.
type Contact struct {
	ID          int64
	Name        string
	PhoneNumber string
	Identifier  string
	AvatarURL   string
}

// Resolver finds or creates Chatwoot contacts through client.
type Resolver struct {
	client      *client.Client
	mergeBrazil bool
	log         *slog.Logger
}

// New builds a Resolver. MergeBrazil mirrors
// model.ChatwootConfig.MergeBrazilContacts. A nil log discards output.
func New(c *client.Client, cfg model.ChatwootConfig, log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Resolver{client: c, mergeBrazil: cfg.MergeBrazilContacts, log: log}
}

// Resolve returns the Chatwoot contact for a sender, creating (and updating
// name/avatar when empty or divergent) as needed. Individuals resolve by
// phone through the filter API; groups resolve by JID identifier through
// search. A creation failure logs a warn and returns nil with the error so
// the caller can skip the message without killing the worker.
func (r *Resolver) Resolve(ctx context.Context, phone string, isGroup bool, name, avatar, jid string) (*Contact, error) {
	if isGroup {
		return r.resolveGroup(ctx, name, avatar, jid)
	}
	digits := stripDigits(phone)
	if digits == "" {
		return nil, fmt.Errorf("contacts: empty phone")
	}
	e164 := "+" + digits
	// Union lookup: client.FindContactByPhone takes a single phone per call,
	// so each BR variant is searched separately (2 calls max per BR lookup)
	// and the hits are unioned with dedup by id; longest-wins/merge below
	// gives the OR semantic.
	var found []client.Contact
	seen := map[int64]bool{}
	for _, variant := range variants(e164) {
		matches, err := r.client.FindContactByPhone(ctx, strings.TrimPrefix(variant, "+"))
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			if !seen[match.ID] {
				seen[match.ID] = true
				found = append(found, match)
			}
		}
	}
	if len(found) == 0 {
		return r.createIndividual(ctx, e164, name, avatar, jid)
	}
	base := longestPhone(found)
	if len(found) > 1 && r.mergeBrazil && isBrazilian(digits) {
		base = r.mergeDuplicates(ctx, base, found)
	}
	return r.ensureFresh(ctx, base, name, avatar)
}

// resolveGroup resolves a group by its JID identifier, creating it with a
// "(GROUP)" name suffix when absent.
func (r *Resolver) resolveGroup(ctx context.Context, name, avatar, jid string) (*Contact, error) {
	if strings.TrimSpace(jid) == "" {
		return nil, fmt.Errorf("contacts: empty group jid")
	}
	matches, err := r.client.SearchContacts(ctx, jid)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return r.createGroup(ctx, name, avatar, jid)
	}
	return r.ensureFresh(ctx, matches[0], groupName(name, jid), avatar)
}

// createIndividual creates a people contact with the +E.164 phone number. A
// 422 means the number is taken under another record: with a jid hint it
// falls back to an identifier search before giving up.
func (r *Resolver) createIndividual(ctx context.Context, e164, name, avatar, jid string) (*Contact, error) {
	req := client.CreateContactRequest{
		Name:        fallbackName(name, e164),
		PhoneNumber: e164,
	}
	if avatar != "" {
		req.AvatarURL = avatar
	}
	created, err := r.client.CreateContact(ctx, req)
	if err != nil {
		if isUnprocessable(err) && strings.TrimSpace(jid) != "" {
			if matches, serr := r.client.SearchContacts(ctx, jid); serr == nil && len(matches) > 0 {
				r.log.Info("contact create conflict recovered by identifier search", "phone", e164)
				return r.ensureFresh(ctx, matches[0], name, avatar)
			}
		}
		r.log.Warn("contact creation failed, skipping message mirror", "phone", e164, "error", err)
		return nil, err
	}
	return fromClient(created), nil
}

// createGroup creates a group contact keyed by its JID identifier.
func (r *Resolver) createGroup(ctx context.Context, name, avatar, jid string) (*Contact, error) {
	req := client.CreateContactRequest{
		Name:       groupName(name, jid),
		Identifier: jid,
	}
	if avatar != "" {
		req.AvatarURL = avatar
	}
	created, err := r.client.CreateContact(ctx, req)
	if err != nil {
		r.log.Warn("group contact creation failed, skipping message mirror", "jid", jid, "error", err)
		return nil, err
	}
	return fromClient(created), nil
}

// mergeDuplicates folds every duplicate into base, keeping the longest phone
// record. A single failing merge only warns; resolution continues with base.
func (r *Resolver) mergeDuplicates(ctx context.Context, base client.Contact, found []client.Contact) client.Contact {
	for _, other := range found {
		if other.ID == base.ID {
			continue
		}
		merged, err := r.client.MergeContacts(ctx, client.MergeContactsRequest{
			BaseContactID:   base.ID,
			MergeeContactID: other.ID,
		})
		if err != nil {
			r.log.Warn("contact merge failed", "base_id", base.ID, "mergee_id", other.ID, "error", err)
			continue
		}
		if merged != nil {
			base = *merged
		}
	}
	return base
}

// ensureFresh updates name/avatar when the stored values are empty or
// divergent. An update failure only warns: resolution already succeeded and
// the next message retries the cosmetic fields.
func (r *Resolver) ensureFresh(ctx context.Context, existing client.Contact, name, avatar string) (*Contact, error) {
	var req client.UpdateContactRequest
	if name != "" && existing.Name != name {
		req.Name = name
	}
	if avatar != "" && existing.AvatarURL != avatar {
		req.AvatarURL = avatar
	}
	if req == (client.UpdateContactRequest{}) {
		return fromClient(&existing), nil
	}
	updated, err := r.client.UpdateContact(ctx, existing.ID, req)
	if err != nil {
		r.log.Warn("contact update failed", "contact_id", existing.ID, "error", err)
		return fromClient(&existing), nil
	}
	return fromClient(updated), nil
}

// variants returns the lookup forms of an +E.164 phone: itself plus the
// Brazilian alternate with/without the 9th digit when one exists.
func variants(phone string) []string {
	digits := stripDigits(phone)
	if digits == "" {
		return []string{phone}
	}
	base := "+" + digits
	if alt := brazilianAlternate(digits); alt != "" {
		return []string{base, "+" + alt}
	}
	return []string{base}
}

// brazilianAlternate returns the alternate form of a Brazilian mobile: a
// 55-prefixed 13-digit number drops the 9, and a 55-prefixed 12-digit mobile
// starting with 6-9 gains it. Landlines and foreign numbers have none.
func brazilianAlternate(digits string) string {
	if !strings.HasPrefix(digits, "55") {
		return ""
	}
	switch len(digits) {
	case 13:
		if digits[4] != '9' {
			return ""
		}
		return digits[:4] + digits[5:]
	case 12:
		if digits[4] < '6' || digits[4] > '9' {
			return ""
		}
		return digits[:4] + "9" + digits[4:]
	default:
		return ""
	}
}

// isBrazilian reports whether digits are a Brazilian (+55) number.
func isBrazilian(digits string) bool {
	return strings.HasPrefix(digits, "55")
}

// longestPhone picks the duplicate base: longest phone number, smallest id on
// ties for determinism.
func longestPhone(found []client.Contact) client.Contact {
	base := found[0]
	for _, c := range found[1:] {
		if len(c.PhoneNumber) > len(base.PhoneNumber) ||
			(len(c.PhoneNumber) == len(base.PhoneNumber) && c.ID < base.ID) {
			base = c
		}
	}
	return base
}

// groupName renders the display name of a group contact, never doubling the
// suffix and falling back to the jid when nameless.
func groupName(name, jid string) string {
	display := fallbackName(name, jid)
	if strings.HasSuffix(display, groupSuffix) {
		return display
	}
	return display + groupSuffix
}

// fallbackName keeps name unless blank, then uses fallback.
func fallbackName(name, fallback string) string {
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return name
}

// stripDigits keeps only the digits of s.
func stripDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// isUnprocessable reports whether err is a Chatwoot 422 response.
func isUnprocessable(err error) bool {
	var cerr *client.Error
	return errors.As(err, &cerr) && cerr.Status == 422
}

// fromClient maps the client projection to the resolver projection.
func fromClient(c *client.Contact) *Contact {
	if c == nil {
		return nil
	}
	return &Contact{
		ID:          c.ID,
		Name:        c.Name,
		PhoneNumber: c.PhoneNumber,
		Identifier:  c.Identifier,
		AvatarURL:   c.AvatarURL,
	}
}
