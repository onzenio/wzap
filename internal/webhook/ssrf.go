package webhook

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// maxWebhookRedirects capa os saltos seguidos num POST de webhook. O cap é
// aplicado junto à política SSRF, então uma URL pública não pode quicar para
// metadata/link-local.
const maxWebhookRedirects = 3

// lookupIP resolve hostname para endereços na política SSRF. Variável para
// testes injetarem resolução sem rede.
var lookupIP = net.LookupIP

var (
	// metadataIPv4 são endpoints de metadata sempre bloqueados, mesmo se um
	// dia coincidirem com host legítimo.
	metadataIPv4 = []net.IP{
		net.ParseIP("169.254.169.254"),
		net.ParseIP("100.100.100.200"),
	}
	// linkLocalNets sempre bloqueadas.
	linkLocalNets = []net.IPNet{
		{IP: net.ParseIP("169.254.0.0"), Mask: net.CIDRMask(16, 32)},
		{IP: net.ParseIP("fe80::"), Mask: net.CIDRMask(10, 128)},
	}
	// carrierGradeNAT bloqueada para hosts não-loopback (carrega metadata da Alibaba).
	carrierGradeNAT = net.IPNet{IP: net.ParseIP("100.64.0.0"), Mask: net.CIDRMask(10, 32)}
)

// validateWebhookURL aplica o gate SSRF do webhook outbound: scheme
// http/https apenas, sem userinfo, host presente, metadata/link-local sempre
// bloqueados, loopback liberado (dev), qualquer outro não-público bloqueado.
func validateWebhookURL(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return fmt.Errorf("webhook URL rejected: empty url")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("webhook URL rejected: unparseable url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook URL rejected: scheme %q is not http or https", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("webhook URL rejected: userinfo is not allowed")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return fmt.Errorf("webhook URL rejected: empty host")
	}
	ips, err := resolveWebhookHost(host)
	if err != nil {
		return fmt.Errorf("webhook URL rejected: cannot resolve host %q", host)
	}
	for _, ip := range ips {
		if isWebhookAlwaysBlocked(ip) {
			return fmt.Errorf("webhook URL rejected: host %q resolves to blocked address", host)
		}
	}
	if isLoopbackHost(host) || allLoopback(ips) {
		return nil
	}
	for _, ip := range ips {
		if isWebhookBlockedIP(ip) {
			return fmt.Errorf("webhook URL rejected: host %q resolves to non-public address", host)
		}
	}
	return nil
}

// resolveWebhookHost retorna os IPs do host sem rede para literais, via
// lookupIP para nomes.
func resolveWebhookHost(host string) ([]net.IP, error) {
	if ip := net.ParseIP(strings.TrimSuffix(host, ".")); ip != nil {
		return []net.IP{ip}, nil
	}
	ips, err := lookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("resolve %q: no addresses", host)
	}
	return ips, nil
}

// isWebhookAlwaysBlocked reporta endereços que nenhum webhook pode alcançar.
func isWebhookAlwaysBlocked(ip net.IP) bool {
	for _, meta := range metadataIPv4 {
		if ip.Equal(meta) {
			return true
		}
	}
	for _, network := range linkLocalNets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// isWebhookBlockedIP reporta não-públicos: loopback é tratado pelo chamador
// (allowlist de dev); aqui tudo o mais privado/link-local/multicast/
// unspecified/CGNAT bloqueia. Loopback também retorna true para que um host
// misto (público+loopback) não passe como público puro — o allowlist só vale
// quando todos os IPs são loopback.
func isWebhookBlockedIP(ip net.IP) bool {
	switch {
	case ip.IsUnspecified(),
		ip.IsLoopback(),
		ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsMulticast(),
		ip.IsPrivate():
		return true
	}
	return carrierGradeNAT.Contains(ip)
}

// allLoopback reporta se todos os IPs são loopback (caso dev).
func allLoopback(ips []net.IP) bool {
	if len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false
		}
	}
	return true
}

// isResolveError reporta falha de DNS: config valida fail-open, delivery
// bloqueia fail-closed.
func isResolveError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "cannot resolve host")
}

// pinnedDialer resolve uma vez, valida contra a política SSRF e disca o IP
// validado — sem segunda resolução no Dial, fechando o TOCTOU entre
// validate e connect.
func pinnedDialer(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSuffix(strings.ToLower(host), ".")
	var ips []net.IP
	if ip := net.ParseIP(trimmed); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = lookupIP(trimmed)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("webhook dial rejected: cannot resolve host %q", host)
		}
	}
	for _, ip := range ips {
		if isWebhookAlwaysBlocked(ip) {
			return nil, fmt.Errorf("webhook dial rejected: host %q resolves to blocked address", host)
		}
	}
	if !(isLoopbackHost(trimmed) || allLoopback(ips)) {
		for _, ip := range ips {
			if isWebhookBlockedIP(ip) {
				return nil, fmt.Errorf("webhook dial rejected: host %q resolves to non-public address", host)
			}
		}
	}
	dialer := &net.Dialer{}
	// Disca o primeiro IP validado: o pinning usa a mesma resolução
	// validada acima, sem re-resolver no transporte.
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

// checkWebhookRedirect valida cada hop de redirect contra o gate SSRF e capa
// a cadeia em maxWebhookRedirects.
func checkWebhookRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxWebhookRedirects {
		return fmt.Errorf("webhook redirect rejected: too many redirects")
	}
	if err := validateWebhookURL(req.URL.String()); err != nil {
		return err
	}
	return nil
}
