// Package mapper holds pure WhatsApp <-> Chatwoot content mappings.
//
// Every function here is a pure table/format conversion with no I/O, so the
// mirror worker can consume it without side effects. Formatting follows the
// Evolution parity: WhatsApp uses *bold*, _italic_ and ~strike~ while
// Chatwoot renders **bold**, *italic* and ~~strike~~.
package mapper

import (
	"regexp"
	"strconv"
	"strings"
)

// TextInput carries the text-bearing fields of an inbound WhatsApp message.
// Body is the plain/extended text; Caption accompanies a media message.
type TextInput struct {
	Body    string
	Caption string
}

// Text resolves the display text of a message, preferring Body and falling
// back to Caption (media without text yields its caption).
func Text(in TextInput) string {
	if in.Body != "" {
		return in.Body
	}
	return in.Caption
}

const placeholder = "\x00"

var (
	chatwootBoldRe   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	chatwootStrikeRe = regexp.MustCompile(`~~([^~\n]+)~~`)
	whatsBoldRe      = regexp.MustCompile(`\*([^*\n]+)\*`)
	whatsItalicRe    = regexp.MustCompile(`_([^_\n]+)_`)
	whatsStrikeRe    = regexp.MustCompile(`~([^~\n]+)~`)
)

// protect replaces every match of re with an indexed placeholder namespaced
// by tag and returns the rewritten string plus the captured inner texts.
func protect(s, tag string, re *regexp.Regexp) (string, []string) {
	var inners []string
	out := re.ReplaceAllStringFunc(s, func(m string) string {
		sub := re.FindStringSubmatch(m)
		inner := m
		if len(sub) == 2 {
			inner = sub[1]
		}
		inners = append(inners, inner)
		return placeholder + tag + strconv.Itoa(len(inners)-1) + placeholder
	})
	return out, inners
}

// restore swaps placeholders back, wrapping each inner text with wrap.
func restore(s, tag string, inners []string, wrap func(inner string) string) string {
	for i, inner := range inners {
		s = strings.ReplaceAll(s, placeholder+tag+strconv.Itoa(i)+placeholder, wrap(inner))
	}
	return s
}

// MarkdownToChatwoot converts WhatsApp formatting (*bold*, _italic_,
// ~strike~) to Chatwoot markdown (**bold**, *italic*, ~~strike~~).
// Spans already in Chatwoot form are left untouched.
func MarkdownToChatwoot(s string) string {
	protected, bolds := protect(s, "B", chatwootBoldRe)
	protected, strikes := protect(protected, "S", chatwootStrikeRe)
	protected = whatsBoldRe.ReplaceAllString(protected, `**$1**`)
	protected = whatsItalicRe.ReplaceAllString(protected, `*$1*`)
	protected = whatsStrikeRe.ReplaceAllString(protected, `~~$1~~`)
	protected = restore(protected, "S", strikes, func(inner string) string { return "~~" + inner + "~~" })
	return restore(protected, "B", bolds, func(inner string) string { return "**" + inner + "**" })
}

// MarkdownToWhatsApp converts Chatwoot markdown (**bold**, *italic*,
// ~~strike~~) back to WhatsApp formatting (*bold*, _italic_, ~strike~).
// Underscore italics are already valid WhatsApp and pass through.
func MarkdownToWhatsApp(s string) string {
	protected, strikes := protect(s, "S", chatwootStrikeRe)
	protected, bolds := protect(protected, "B", chatwootBoldRe)
	protected = whatsBoldRe.ReplaceAllString(protected, `_${1}_`)
	protected = restore(protected, "B", bolds, func(inner string) string { return "*" + inner + "*" })
	return restore(protected, "S", strikes, func(inner string) string { return "~" + inner + "~" })
}

// formatCoord renders a coordinate in shortest round-trip form for URLs.
func formatCoord(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// Location formats a shared location as name/address lines plus a Google
// Maps link. Empty name/address lines are omitted; the link is always last.
func Location(lat, lng float64, name, addr string) string {
	lines := make([]string, 0, 3)
	if name != "" {
		lines = append(lines, name)
	}
	if addr != "" {
		lines = append(lines, addr)
	}
	lines = append(lines, "https://maps.google.com/?q="+formatCoord(lat)+","+formatCoord(lng))
	return strings.Join(lines, "\n")
}

// Contact formats a shared contact as the display name followed by one phone
// number per line. Empty fields are omitted.
func Contact(fn string, tels []string) string {
	lines := make([]string, 0, len(tels)+1)
	if fn != "" {
		lines = append(lines, fn)
	}
	for _, tel := range tels {
		if tel != "" {
			lines = append(lines, tel)
		}
	}
	return strings.Join(lines, "\n")
}

// GroupPrefix prefixes a group message body with "phone — name" so the
// sender is visible in the mirrored conversation. An empty body yields just
// the prefix.
func GroupPrefix(phone, name, body string) string {
	prefix := phone
	if name != "" {
		prefix += " \u2014 " + name
	}
	if body == "" {
		return prefix
	}
	return prefix + "\n" + body
}

// ListRow is one selectable row of a list message.
type ListRow struct {
	Title       string
	Description string
}

// ListSection groups list rows under a section title.
type ListSection struct {
	Title string
	Rows  []ListRow
}

// List formats a list/selection message as text: title, description, one
// bullet per row (description after an em dash), the button label in
// brackets and the footer last. Empty parts are omitted; everything empty
// yields "" so the caller keeps the warn+skip path.
func List(title, description, button, footer string, sections []ListSection) string {
	lines := make([]string, 0, 8)
	if title != "" {
		lines = append(lines, title)
	}
	if description != "" {
		lines = append(lines, description)
	}
	for _, section := range sections {
		if section.Title != "" {
			lines = append(lines, section.Title+":")
		}
		for _, row := range section.Rows {
			if row.Title == "" && row.Description == "" {
				continue
			}
			line := "\u2022 " + row.Title
			if row.Description != "" {
				line += " \u2014 " + row.Description
			}
			lines = append(lines, line)
		}
	}
	if button != "" {
		lines = append(lines, "["+button+"]")
	}
	if footer != "" {
		lines = append(lines, footer)
	}
	return strings.Join(lines, "\n")
}

// ListResponse formats a list/button answer as text: the visible title plus
// the selected row or button id so the choice stays traceable.
func ListResponse(title, selectedID string) string {
	switch {
	case title != "" && selectedID != "":
		return title + "\nResposta: " + selectedID
	case title != "":
		return title
	default:
		return "Resposta: " + selectedID
	}
}

// Reaction formats a reaction as descriptive text linked to the original
// message key. An empty emoji means the reaction was removed.
func Reaction(emoji, originalKey string) string {
	if emoji == "" {
		if originalKey == "" {
			return "Rea\u00e7\u00e3o removida"
		}
		return "Rea\u00e7\u00e3o removida da mensagem " + originalKey
	}
	if originalKey == "" {
		return "Reagiu com " + emoji
	}
	return "Reagiu com " + emoji + " \u00e0 mensagem " + originalKey
}

// Interactive formats buttons/interactive content (including PIX keys sent
// as interactive or native-flow buttons) as text: header, body, footer plus
// one bullet per button label. Empty parts are omitted.
func Interactive(header, body, footer string, buttons []string) string {
	lines := make([]string, 0, len(buttons)+3)
	if header != "" {
		lines = append(lines, header)
	}
	if body != "" {
		lines = append(lines, body)
	}
	for _, button := range buttons {
		if button != "" {
			lines = append(lines, "\u2022 "+button)
		}
	}
	if footer != "" {
		lines = append(lines, footer)
	}
	return strings.Join(lines, "\n")
}

// Order formats a payment/order message as text with its order id.
func Order(orderID, title string) string {
	lines := make([]string, 0, 2)
	if title != "" {
		lines = append(lines, title)
	}
	if orderID != "" {
		lines = append(lines, "Pedido "+orderID)
	}
	return strings.Join(lines, "\n")
}

// Product formats a product message as text: title plus description. Both
// empty yields "" so the caller keeps the warn+skip path.
func Product(title, description string) string {
	lines := make([]string, 0, 2)
	if title != "" {
		lines = append(lines, title)
	}
	if description != "" {
		lines = append(lines, description)
	}
	return strings.Join(lines, "\n")
}

// Ad formats a click-to-WhatsApp ad preview as text: title, body and source
// URL. The preview thumbnail (when present) travels separately as an
// attachment; an ad with no text at all yields "" and still mirrors through
// the thumbnail.
func Ad(title, body, sourceURL string) string {
	lines := make([]string, 0, 3)
	if title != "" {
		lines = append(lines, title)
	}
	if body != "" {
		lines = append(lines, body)
	}
	if sourceURL != "" {
		lines = append(lines, sourceURL)
	}
	return strings.Join(lines, "\n")
}

// drawingExts are image extensions that Chatwoot cannot preview inline, so
// they must travel as generic documents even though their MIME is image/*.
var drawingExts = map[string]bool{
	".svg":  true,
	".svgz": true,
	".ai":   true,
	".eps":  true,
	".psd":  true,
}

// AttachmentKind maps a MIME type plus file extension to the Chatwoot
// attachment kind ("audio", "video", "image" or "file"). asDocument reports
// whether the attachment must be sent as a generic document: true for
// drawing extensions (e.g. .svg) and for any non-media MIME type.
func AttachmentKind(mime, ext string) (kind string, asDocument bool) {
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = mime[:i]
	}
	mime = strings.ToLower(strings.TrimSpace(mime))
	ext = strings.ToLower(ext)
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if drawingExts[ext] {
		return "file", true
	}
	switch {
	case strings.HasPrefix(mime, "audio/"):
		return "audio", false
	case strings.HasPrefix(mime, "video/"):
		return "video", false
	case strings.HasPrefix(mime, "image/"):
		return "image", false
	default:
		return "file", true
	}
}
