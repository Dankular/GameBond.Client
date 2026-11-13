// Package hotspot implements guest authentication and packet authorization for
// the LAN-device gateway.
package hotspot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gamebond.local/nativeedge/atomicio"
	"golang.org/x/crypto/argon2"
)

type Config struct {
	Enabled         bool          `json:"enabled"`
	Devices         []string      `json:"devices"`
	Listen          string        `json:"listen"`
	Hostname        string        `json:"hostname"`
	TLSCert         string        `json:"tls_cert"`
	TLSKey          string        `json:"tls_key"`
	BrandName       string        `json:"brand_name"`
	LogoFile        string        `json:"logo_file"`
	PrimaryColor    string        `json:"primary_color"`
	TermsURL        string        `json:"terms_url"`
	SessionSeconds  int           `json:"session_seconds"`
	PasswordHash    string        `json:"password_hash"`
	Vouchers        []VoucherSpec `json:"vouchers"`
	ExternalURL     string        `json:"external_url"`
	ExternalSecret  string        `json:"external_secret"`
	PaymentEndpoint string        `json:"payment_endpoint"`
	PaymentSecret   string        `json:"payment_secret"`
	PaymentWebhook  string        `json:"payment_webhook_secret"`
	PaymentPlan     string        `json:"payment_plan"`
	PaymentCurrency string        `json:"payment_currency"`
	PaymentAmount   int64         `json:"payment_amount_minor"`
}

// ResolveSecretReferences replaces file:/path references with secret contents.
// Relative paths are resolved from the client's configuration directory.
func ResolveSecretReferences(c *Config, root string) error {
	if c == nil {
		return errors.New("nil hotspot configuration")
	}
	for _, ref := range []*string{&c.ExternalSecret, &c.PaymentSecret, &c.PaymentWebhook} {
		if !strings.HasPrefix(*ref, "file:") {
			continue
		}
		name := strings.TrimPrefix(*ref, "file:")
		if name == "" {
			return errors.New("empty hotspot secret file reference")
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(root, name)
		}
		info, err := os.Stat(name)
		if err != nil {
			return fmt.Errorf("read hotspot secret file: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() > 16<<10 {
			return errors.New("hotspot secret file must be a regular file no larger than 16 KiB")
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		*ref = strings.TrimSuffix(string(data), "\n")
		*ref = strings.TrimSuffix(*ref, "\r")
		if *ref == "" {
			return errors.New("hotspot secret file is empty")
		}
	}
	for _, path := range []*string{&c.TLSCert, &c.TLSKey, &c.LogoFile} {
		if *path != "" && !filepath.IsAbs(*path) {
			*path = filepath.Join(root, *path)
		}
	}
	return nil
}

type VoucherSpec struct {
	Hash      string `json:"hash"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	Uses      int    `json:"uses"`
	Duration  int    `json:"duration_seconds,omitempty"`
}

func (v VoucherSpec) DurationOr(fallback int) int {
	if v.Duration > 0 {
		return v.Duration
	}
	return fallback
}

// Validate rejects incomplete enabled configurations before a listener or
// gateway is started. Secrets should be populated from the operator's secret
// store before this function is called.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.Devices) == 0 || len(c.Devices) > 4 {
		return errors.New("enabled hotspot requires 1 to 4 guest device names")
	}
	seenDevices := map[string]bool{}
	for _, name := range c.Devices {
		if name == "" || len(name) > 32 || seenDevices[name] {
			return errors.New("hotspot devices must contain unique configured device names")
		}
		seenDevices[name] = true
	}
	if c.Listen == "" || c.Hostname == "" || c.TLSCert == "" || c.TLSKey == "" {
		return errors.New("enabled hotspot requires listen, hostname, tls_cert and tls_key")
	}
	_, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return errors.New("hotspot listen must be host:port")
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return errors.New("hotspot listen port must be between 1 and 65535")
	}
	if strings.ContainsAny(c.Hostname, "/\\\x00 \t\r\n") {
		return errors.New("hotspot hostname must be a DNS name or IP address")
	}
	if c.SessionSeconds < 60 || c.SessionSeconds > 30*24*60*60 {
		return errors.New("hotspot session_seconds must be between 60 and 2592000")
	}
	if c.BrandName == "" {
		return errors.New("hotspot brand_name is required")
	}
	if c.PrimaryColor != "" {
		if len(c.PrimaryColor) != 7 || c.PrimaryColor[0] != '#' {
			return errors.New("hotspot primary_color must be #RRGGBB")
		}
		for _, ch := range c.PrimaryColor[1:] {
			if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
				return errors.New("hotspot primary_color must be #RRGGBB")
			}
		}
	}
	if strings.ContainsAny(c.BrandName, "<>\x00") || len(c.BrandName) > 80 {
		return errors.New("hotspot brand_name contains invalid characters or is too long")
	}
	if c.TermsURL != "" && !(strings.HasPrefix(c.TermsURL, "https://") || strings.HasPrefix(c.TermsURL, "/")) {
		return errors.New("hotspot terms_url must use HTTPS or a local path")
	}
	if c.ExternalURL != "" && !secureURL(c.ExternalURL) {
		return errors.New("hotspot external_url must use HTTPS")
	}
	if c.ExternalURL != "" && (c.ExternalSecret == "" || (!strings.HasPrefix(c.ExternalSecret, "file:") && len(c.ExternalSecret) < 32)) {
		return errors.New("hotspot external portal requires external_secret")
	}
	if c.PaymentEndpoint != "" && !secureURL(c.PaymentEndpoint) {
		return errors.New("hotspot payment_endpoint must use HTTPS")
	}
	if c.PaymentEndpoint != "" && (c.PaymentSecret == "" || c.PaymentWebhook == "" || c.PaymentPlan == "" || c.PaymentCurrency == "" || c.PaymentAmount <= 0) {
		return errors.New("hotspot payment requires endpoint, secret, webhook secret, plan, currency and positive amount")
	}
	if c.PaymentEndpoint != "" && ((!strings.HasPrefix(c.PaymentSecret, "file:") && len(c.PaymentSecret) < 32) || (!strings.HasPrefix(c.PaymentWebhook, "file:") && len(c.PaymentWebhook) < 32)) {
		return errors.New("hotspot payment secrets must be at least 32 bytes or file references")
	}
	if c.PaymentCurrency != "" {
		if len(c.PaymentCurrency) != 3 {
			return errors.New("hotspot payment_currency must be a three-letter code")
		}
		for _, ch := range c.PaymentCurrency {
			if ch < 'A' || ch > 'Z' {
				return errors.New("hotspot payment_currency must be uppercase ASCII")
			}
		}
	}
	if len(c.PaymentPlan) > 64 || strings.ContainsAny(c.PaymentPlan, "\x00\r\n") {
		return errors.New("hotspot payment_plan is invalid")
	}
	if len(c.Vouchers) == 0 && c.PasswordHash == "" && c.ExternalURL == "" && c.PaymentEndpoint == "" {
		return errors.New("hotspot requires at least one authentication method")
	}
	return nil
}

func secureURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

type voucher struct {
	spec VoucherSpec
	uses int
}
type guestSession struct {
	expires time.Time
	cookie  string
}

// Manager holds only hashes for vouchers and passwords and binds access to a
// fixed guest IPv4 lease. All state is in-memory and therefore expires on restart.
type Manager struct {
	mu             sync.Mutex
	cfg            Config
	passwordSalt   []byte
	vouchers       map[string]*voucher
	sessions       map[netip.Addr]guestSession
	cookies        map[string]netip.Addr
	events         []Event
	allowedPublic  map[netip.Addr]bool
	bootstrapHosts map[netip.Addr]time.Time
	failures       map[netip.Addr]loginFailure
	statePath      string
	hashSlots      chan struct{}
}

type loginFailure struct {
	count int
	retry time.Time
}

type Event struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Guest  string    `json:"guest,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

var deniedV4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
}

func New(c Config) (*Manager, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	m := &Manager{cfg: c, vouchers: map[string]*voucher{}, sessions: map[netip.Addr]guestSession{}, cookies: map[string]netip.Addr{}, allowedPublic: map[netip.Addr]bool{}, bootstrapHosts: map[netip.Addr]time.Time{}, failures: map[netip.Addr]loginFailure{}, hashSlots: make(chan struct{}, 2)}
	for _, host := range []string{c.Hostname, endpointHost(c.ExternalURL), endpointHost(c.PaymentEndpoint)} {
		if host == "" {
			continue
		}
		if ip, err := netip.ParseAddr(host); err == nil && ip.Is4() {
			m.allowedPublic[ip] = true
			continue
		}
		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, fmt.Errorf("resolve hotspot host %q: %w", host, err)
		}
		found := false
		for _, ip := range ips {
			if a, ok := netip.AddrFromSlice(ip); ok && a.Is4() && !a.IsPrivate() {
				m.allowedPublic[a] = true
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("hotspot host %q has no public IPv4 address", host)
		}
	}
	seenVoucher := map[string]bool{}
	for _, v := range c.Vouchers {
		if len(v.Hash) != 64 {
			return nil, errors.New("hotspot voucher hash must be a SHA-256 hex digest")
		}
		if _, err := hex.DecodeString(v.Hash); err != nil {
			return nil, errors.New("invalid hotspot voucher hash")
		}
		if seenVoucher[strings.ToLower(v.Hash)] {
			return nil, errors.New("duplicate hotspot voucher hash")
		}
		seenVoucher[strings.ToLower(v.Hash)] = true
		if v.Uses < 1 || v.Uses > 100000 {
			return nil, errors.New("hotspot voucher uses must be between 1 and 100000")
		}
		if v.Duration < 0 || v.Duration > c.SessionSeconds {
			return nil, errors.New("hotspot voucher duration must be within the session limit")
		}
		m.vouchers[strings.ToLower(v.Hash)] = &voucher{spec: v}
	}
	if c.PasswordHash != "" {
		if _, err := decodePasswordHash(c.PasswordHash); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// HashPassword creates an encoded Argon2id password hash for config storage.
func HashPassword(password string) (string, error) {
	if len(password) < 10 || len(password) > 1024 {
		return "", errors.New("password must contain 10 to 1024 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, 2, 64*1024, 2, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=2,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func decodePasswordHash(s string) ([]byte, error) {
	parts := strings.Split(s, "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" || parts[2] != "m=65536,t=2,p=2" {
		return nil, errors.New("hotspot password_hash must be an Argon2id hash from gamebond hotspot password")
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[3])
	key, e2 := base64.RawStdEncoding.DecodeString(parts[4])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(key) != 32 {
		return nil, errors.New("invalid hotspot password_hash")
	}
	return append(salt, key...), nil
}

func (m *Manager) passwordOK(password string) bool {
	encoded := m.cfg.PasswordHash
	if encoded == "" {
		return false
	}
	raw, err := decodePasswordHash(encoded)
	if err != nil {
		return false
	}
	select {
	case m.hashSlots <- struct{}{}:
		defer func() { <-m.hashSlots }()
	default:
		return false
	}
	got := argon2.IDKey([]byte(password), raw[:16], 2, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, raw[16:]) == 1
}

func voucherDigest(code string) string {
	h := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(code))))
	return hex.EncodeToString(h[:])
}

func VoucherDigest(code string) string { return voucherDigest(code) }

func GenerateVoucher(uses int, expires time.Time, duration int) (string, VoucherSpec, error) {
	if uses < 1 || uses > 100000 || duration < 0 {
		return "", VoucherSpec{}, errors.New("invalid voucher limits")
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", VoucherSpec{}, err
	}
	code := strings.TrimRight(base32.StdEncoding.EncodeToString(raw), "=")
	spec := VoucherSpec{Hash: voucherDigest(code), Uses: uses, Duration: duration}
	if !expires.IsZero() {
		spec.ExpiresAt = expires.Unix()
	}
	return code, spec, nil
}

func (m *Manager) redeemVoucher(ip netip.Addr, code string, now time.Time) bool {
	if len(code) > 128 {
		m.mu.Lock()
		m.noteFailureLocked(ip, now)
		m.mu.Unlock()
		return false
	}
	digest := voucherDigest(code)
	m.mu.Lock()
	defer m.mu.Unlock()
	if f := m.failures[ip]; now.Before(f.retry) {
		return false
	}
	v := m.vouchers[digest]
	if v == nil || (v.spec.ExpiresAt > 0 && now.Unix() >= v.spec.ExpiresAt) || v.uses >= v.spec.Uses {
		m.noteFailureLocked(ip, now)
		return false
	}
	v.uses++
	if err := m.persistLocked(); err != nil {
		v.uses--
		return false
	}
	cookie := m.grantLocked(ip, time.Duration(v.spec.DurationOr(m.cfg.SessionSeconds))*time.Second, now)
	if cookie == "" {
		v.uses--
		_ = m.persistLocked()
		return false
	}
	delete(m.failures, ip)
	m.eventLocked("voucher_redeemed", ip, "")
	return true
}

func (m *Manager) loginPassword(ip netip.Addr, password string, now time.Time) bool {
	m.mu.Lock()
	f := m.failures[ip]
	retry := f.retry
	m.mu.Unlock()
	if now.Before(retry) {
		return false
	}
	if len(password) > 1024 || !m.passwordOK(password) {
		m.mu.Lock()
		m.noteFailureLocked(ip, now)
		m.eventLocked("login_failed", ip, "")
		m.mu.Unlock()
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.grantLocked(ip, time.Duration(m.cfg.SessionSeconds)*time.Second, now) == "" {
		return false
	}
	delete(m.failures, ip)
	m.eventLocked("password_login", ip, "")
	return true
}

func (m *Manager) grantLocked(ip netip.Addr, duration time.Duration, now time.Time) string {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return ""
	}
	cookie := base64.RawURLEncoding.EncodeToString(token)
	m.sessions[ip] = guestSession{expires: now.Add(duration), cookie: cookie}
	m.cookies[cookie] = ip
	return cookie
}

func (m *Manager) Grant(ip netip.Addr, duration time.Duration, now time.Time, source string) string {
	if !ip.Is4() || duration <= 0 {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if duration > time.Duration(m.cfg.SessionSeconds)*time.Second {
		duration = time.Duration(m.cfg.SessionSeconds) * time.Second
	}
	t := m.grantLocked(ip, duration, now)
	if t != "" {
		m.eventLocked("session_granted", ip, source)
	}
	return t
}

func (m *Manager) Authenticated(ip netip.Addr, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[ip]
	if ok && !now.Before(s.expires) {
		delete(m.sessions, ip)
		delete(m.cookies, s.cookie)
		m.eventLocked("session_expired", ip, "")
		return false
	}
	return ok
}

func (m *Manager) SessionForCookie(cookie string, now time.Time) (netip.Addr, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ip, ok := m.cookies[cookie]
	if !ok {
		return netip.Addr{}, false
	}
	s, exists := m.sessions[ip]
	if !exists || !now.Before(s.expires) {
		delete(m.cookies, cookie)
		delete(m.sessions, ip)
		return netip.Addr{}, false
	}
	return ip, true
}

func (m *Manager) CookieFor(ip netip.Addr, now time.Time) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[ip]
	if !ok || !now.Before(s.expires) {
		return ""
	}
	return s.cookie
}

func (m *Manager) Revoke(ip netip.Addr) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[ip]; ok {
		delete(m.cookies, s.cookie)
		delete(m.sessions, ip)
		m.eventLocked("session_revoked", ip, "")
	}
}

func (m *Manager) WatchRevocations(ctx context.Context, path string) {
	if path == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			claimed := path + ".processing"
			_ = os.Remove(claimed)
			if err := os.Rename(path, claimed); err != nil {
				continue
			}
			b, err := os.ReadFile(claimed)
			_ = os.Remove(claimed)
			if err != nil || len(b) > 16<<10 {
				continue
			}
			var command struct {
				Revoke []string `json:"revoke"`
			}
			if json.Unmarshal(b, &command) != nil || len(command.Revoke) > 64 {
				continue
			}
			for _, raw := range command.Revoke {
				if ip, err := netip.ParseAddr(raw); err == nil && ip.Is4() {
					m.Revoke(ip)
				}
			}
		}
	}()
}

func (m *Manager) eventLocked(kind string, ip netip.Addr, detail string) {
	m.events = append(m.events, Event{At: time.Now().UTC(), Kind: kind, Guest: ip.String(), Detail: detail})
	if len(m.events) > 1000 {
		m.events = append([]Event(nil), m.events[len(m.events)-1000:]...)
	}
}
func (m *Manager) log(kind string, ip netip.Addr) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventLocked(kind, ip, "")
}
func (m *Manager) noteFailureLocked(ip netip.Addr, now time.Time) {
	if _, exists := m.failures[ip]; !exists && len(m.failures) >= 4096 {
		for key := range m.failures {
			delete(m.failures, key)
			break
		}
	}
	f := m.failures[ip]
	f.count++
	shift := f.count - 1
	if shift > 8 {
		shift = 8
	}
	f.retry = now.Add(time.Duration(1<<shift) * time.Second)
	m.failures[ip] = f
}
func (m *Manager) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Event(nil), m.events...)
}
func (m *Manager) IsGuest(name string) bool {
	for _, guest := range m.cfg.Devices {
		if guest == name {
			return true
		}
	}
	return false
}

func (m *Manager) Status(now time.Time) map[string]any {
	return map[string]any{"enabled": m.cfg.Enabled, "guest_devices": append([]string(nil), m.cfg.Devices...), "sessions": m.Sessions(now), "events": m.Events()}
}

// CreateVoucher returns a plaintext voucher exactly once. Only its SHA-256
// digest and limits are retained in memory.
func (m *Manager) CreateVoucher(uses int, expires time.Time, duration int) (string, error) {
	if uses < 1 || uses > 100000 || duration < 0 || duration > m.cfg.SessionSeconds {
		return "", errors.New("invalid voucher limits")
	}
	code, spec, err := GenerateVoucher(uses, expires, duration)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.vouchers[spec.Hash] = &voucher{spec: spec}
	err = m.persistLocked()
	if err != nil {
		delete(m.vouchers, spec.Hash)
	}
	m.mu.Unlock()
	if err != nil {
		return "", err
	}
	return code, nil
}

func (m *Manager) RevokeVoucher(code string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := voucherDigest(code)
	if _, ok := m.vouchers[h]; !ok {
		return false
	}
	v := m.vouchers[h]
	delete(m.vouchers, h)
	if err := m.persistLocked(); err != nil {
		m.vouchers[h] = v
		return false
	}
	return true
}

func (m *Manager) SetStatePath(path string) error {
	if path == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statePath = path
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state struct {
		Uses map[string]int `json:"voucher_uses"`
	}
	if err = json.Unmarshal(b, &state); err != nil {
		return fmt.Errorf("invalid hotspot state: %w", err)
	}
	for hash, uses := range state.Uses {
		if uses < 0 {
			return errors.New("invalid negative voucher use count")
		}
		if v := m.vouchers[strings.ToLower(hash)]; v != nil {
			v.uses = uses
		}
	}
	return nil
}

func (m *Manager) persistLocked() error {
	if m.statePath == "" {
		return nil
	}
	state := struct {
		Uses map[string]int `json:"voucher_uses"`
	}{Uses: map[string]int{}}
	for hash, v := range m.vouchers {
		state.Uses[hash] = v.uses
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicio.WriteFileAtomic(m.statePath, b, 0600)
}

// AllowsDestination implements the opt-in guest gate. Portal and DHCP traffic
// are handled locally by the gateway; this admits its configured DNS gateway
// and public bootstrap destinations before authentication. Authenticated guests
// receive only public IPv4 internet access.
func (m *Manager) AllowsDestination(ip, dst netip.Addr, protocol uint8, dstPort uint16, now time.Time) bool {
	if !m.cfg.Enabled {
		return true
	}
	if !dst.Is4() || dst.IsLoopback() || dst.IsLinkLocalUnicast() || dst.IsMulticast() || dst.IsUnspecified() || isNonPublicV4(dst) {
		return false
	}
	if !m.Authenticated(ip, now) {
		if protocol == 17 && dstPort == 53 && (dst == netip.MustParseAddr("1.1.1.1") || dst == netip.MustParseAddr("8.8.8.8")) {
			return true
		}
		return protocol == 6 && dstPort == 443 && m.allowedBootstrapIP(dst)
	}
	return true
}

func isNonPublicV4(ip netip.Addr) bool {
	for _, prefix := range deniedV4Prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func endpointHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (m *Manager) AllowBootstrapHost(host string) error {
	if host == "" {
		return errors.New("empty hotspot bootstrap host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return err
	}
	var public []netip.Addr
	for _, raw := range ips {
		if ip, ok := netip.AddrFromSlice(raw); ok && ip.Is4() && !isNonPublicV4(ip) {
			public = append(public, ip)
		}
	}
	if len(public) == 0 {
		return errors.New("bootstrap host has no public IPv4 address")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for ip, expires := range m.bootstrapHosts {
		if !now.Before(expires) {
			delete(m.bootstrapHosts, ip)
		}
	}
	if len(m.allowedPublic)+len(m.bootstrapHosts)+len(public) > 64 {
		return errors.New("too many hotspot bootstrap hosts")
	}
	for _, ip := range public {
		m.bootstrapHosts[ip] = now.Add(15 * time.Minute)
	}
	return nil
}

func (m *Manager) allowedBootstrapIP(ip netip.Addr) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.allowedPublic[ip] {
		return true
	}
	expires, ok := m.bootstrapHosts[ip]
	if ok && !time.Now().Before(expires) {
		delete(m.bootstrapHosts, ip)
		return false
	}
	return ok
}

// CheckVoucher is exposed to the portal without exposing voucher hashes.
func (m *Manager) CheckVoucher(ip netip.Addr, code string, now time.Time) bool {
	return m.redeemVoucher(ip, code, now)
}
func (m *Manager) CheckPassword(ip netip.Addr, password string, now time.Time) bool {
	return m.loginPassword(ip, password, now)
}
