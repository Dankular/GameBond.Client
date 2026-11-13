package hotspot

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type Portal struct {
	m               *Manager
	srv             *http.Server
	ln              net.Listener
	mu              sync.Mutex
	used            map[string]time.Time
	pendingExternal map[string]externalState
	pendingPayment  map[string]pendingCheckout
}

type pendingCheckout struct {
	guest   netip.Addr
	created time.Time
}

func (p *Portal) baseURL() string {
	_, port, err := net.SplitHostPort(p.m.cfg.Listen)
	if err != nil || port == "443" {
		return "https://" + p.m.cfg.Hostname
	}
	host := p.m.cfg.Hostname
	if strings.Contains(host, ":") {
		host = net.JoinHostPort(host, port)
	} else {
		host = host + ":" + port
	}
	return "https://" + host
}

var landing = template.Must(template.New("landing").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>{{.Name}}</title><link rel="stylesheet" href="/brand.css"><main class="card">{{if .Logo}}<img class="logo" src="{{.Logo}}" alt="">{{end}}<h1>{{.Name}}</h1><p>Connect to the guest network</p>{{if .Error}}<p class="error">{{.Error}}</p>{{end}}<form method="post" action="/login/voucher"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Access voucher<input name="voucher" autocomplete="one-time-code" required></label><button>Connect with voucher</button></form>{{if .Password}}<form method="post" action="/login/password"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Network password<input name="password" type="password" autocomplete="current-password" required></label><button>Connect with password</button></form>{{end}}{{if .External}}<p><a href="/login/external">Continue to sign in</a></p>{{end}}{{if .Payment}}<form method="post" action="/login/payment"><input type="hidden" name="csrf" value="{{.CSRF}}"><button>Purchase access</button></form>{{end}}{{if .Terms}}<p><a href="{{.Terms}}" rel="noopener">Terms of use</a></p>{{end}}</main></html>`))

func (m *Manager) StartPortal() (*Portal, error) {
	if !m.cfg.Enabled {
		return nil, nil
	}
	addr := m.cfg.Listen
	cert, err := tls.LoadX509KeyPair(m.cfg.TLSCert, m.cfg.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("hotspot TLS certificate: %w", err)
	}
	if len(cert.Certificate) == 0 {
		return nil, fmt.Errorf("hotspot TLS certificate is empty")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("hotspot TLS certificate: %w", err)
	}
	if err = leaf.VerifyHostname(m.cfg.Hostname); err != nil {
		return nil, fmt.Errorf("hotspot TLS certificate does not cover %q: %w", m.cfg.Hostname, err)
	}
	cert.Leaf = leaf
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("hotspot portal listen: %w", err)
	}
	p := &Portal{m: m, ln: ln, used: map[string]time.Time{}, pendingExternal: map[string]externalState{}, pendingPayment: map[string]pendingCheckout{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", p.home)
	mux.HandleFunc("/brand.css", p.brandCSS)
	mux.HandleFunc("/branding/logo", p.logo)
	mux.HandleFunc("/login/voucher", p.voucher)
	mux.HandleFunc("/login/password", p.password)
	mux.HandleFunc("/login/external", p.external)
	mux.HandleFunc("/callback/external", p.externalCallback)
	mux.HandleFunc("/login/payment", p.payment)
	mux.HandleFunc("/callback/payment", p.paymentCallback)
	mux.HandleFunc("/captive-portal", p.home)
	mux.HandleFunc("/generate_204", p.check)
	mux.HandleFunc("/hotspot-detect.html", p.check)
	p.srv = &http.Server{Handler: p.headers(mux), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { _ = p.srv.ServeTLS(ln, "", "") }()
	return p, nil
}

func (p *Portal) Close() error {
	if p == nil || p.srv == nil {
		return nil
	}
	return p.srv.Close()
}

func (p *Portal) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self'; style-src 'self'; form-action 'self https:; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (p *Portal) clientIP(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	return a, err == nil && a.Is4()
}

func (p *Portal) home(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/success" {
		ip, ok := p.clientIP(r)
		if !ok || !p.m.Authenticated(ip, time.Now()) {
			http.Error(w, "session not active", 403)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<!doctype html><meta name=viewport content='width=device-width'><h1>Connected</h1><p>Internet access is now available.</p>")
		return
	}
	if ip, ok := p.clientIP(r); ok && p.m.Authenticated(ip, time.Now()) {
		http.Redirect(w, r, "/success", http.StatusSeeOther)
		return
	}
	color := p.m.cfg.PrimaryColor
	if color == "" {
		color = "#185adb"
	}
	csrfCookie, err := r.Cookie("gb_csrf")
	csrf := ""
	if err == nil && len(csrfCookie.Value) >= 24 {
		csrf = csrfCookie.Value
	} else {
		csrf, err = randomToken()
		if err != nil {
			http.Error(w, "portal unavailable", 503)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "gb_csrf", Value: csrf, Path: "/", Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 900})
	}
	logo := ""
	if p.m.cfg.LogoFile != "" {
		logo = "/branding/logo"
	}
	data := map[string]any{"Name": p.m.cfg.BrandName, "Logo": logo, "Terms": p.m.cfg.TermsURL, "Password": p.m.cfg.PasswordHash != "", "External": p.m.cfg.ExternalURL != "", "Payment": p.m.cfg.PaymentEndpoint != "", "Error": r.URL.Query().Get("error"), "CSRF": csrf}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = landing.Execute(w, data)
}

func (p *Portal) logo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || p.m.cfg.LogoFile == "" {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(p.m.cfg.LogoFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(p.m.cfg.LogoFile)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	typeName := http.DetectContentType(b)
	if typeName != "image/png" && typeName != "image/jpeg" && typeName != "image/webp" {
		http.Error(w, "unsupported logo format", 415)
		return
	}
	w.Header().Set("Content-Type", typeName)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(b)
}

func (p *Portal) brandCSS(w http.ResponseWriter, r *http.Request) {
	color := p.m.cfg.PrimaryColor
	if len(color) != 7 || color[0] != '#' {
		color = "#185adb"
	}
	for _, c := range color[1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			color = "#185adb"
		}
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = io.WriteString(w, "body{font:16px system-ui;margin:0;background:#f3f5f8;color:#182230}.card{max-width:420px;margin:8vh auto;padding:2rem;background:white;border-radius:16px;box-shadow:0 8px 32px #14243b20}h1{color:"+color+"}input,button{box-sizing:border-box;width:100%;padding:.8rem;margin:.35rem 0;border:1px solid #ccd3dd;border-radius:8px}button{background:"+color+";color:white;border:0;font-weight:650;cursor:pointer}.error{color:#a21d28}.logo{max-width:140px;max-height:90px}")
}

func (p *Portal) check(w http.ResponseWriter, r *http.Request) {
	if ip, ok := p.clientIP(r); ok && p.m.Authenticated(ip, time.Now()) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (p *Portal) form(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return false
	}
	cookie, err := r.Cookie("gb_csrf")
	if err != nil || !constantEqual(cookie.Value, r.Form.Get("csrf")) {
		http.Error(w, "invalid form token", http.StatusForbidden)
		return false
	}
	return true
}

func constantEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (p *Portal) success(w http.ResponseWriter, r *http.Request, ip netip.Addr) {
	cookie := p.m.CookieFor(ip, time.Now())
	if cookie == "" {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "gb_guest", Value: cookie, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: p.m.cfg.SessionSeconds})
	http.Redirect(w, r, "/success", http.StatusSeeOther)
}

func (p *Portal) voucher(w http.ResponseWriter, r *http.Request) {
	if !p.form(w, r) {
		return
	}
	ip, ok := p.clientIP(r)
	if !ok {
		http.Error(w, "guest address unavailable", http.StatusForbidden)
		return
	}
	if p.m.CheckVoucher(ip, r.Form.Get("voucher"), time.Now()) {
		p.success(w, r, ip)
		return
	}
	p.m.log("voucher_failed", ip)
	http.Redirect(w, r, "/?error=Invalid+or+expired+voucher", http.StatusSeeOther)
}
func (p *Portal) password(w http.ResponseWriter, r *http.Request) {
	if !p.form(w, r) {
		return
	}
	ip, ok := p.clientIP(r)
	if !ok {
		http.Error(w, "guest address unavailable", http.StatusForbidden)
		return
	}
	if p.m.CheckPassword(ip, r.Form.Get("password"), time.Now()) {
		p.success(w, r, ip)
		return
	}
	http.Redirect(w, r, "/?error=Password+not+accepted", http.StatusSeeOther)
}

func mac(key string, body []byte) []byte {
	m := hmac.New(sha256.New, []byte(key))
	m.Write(body)
	return []byte(hex.EncodeToString(m.Sum(nil)))
}
func validMAC(key string, body []byte, sig string) bool {
	want := mac(key, body)
	return len(sig) == len(want) && subtle.ConstantTimeCompare(want, []byte(strings.ToLower(sig))) == 1
}

type externalState struct {
	Guest  string `json:"guest"`
	Exp    int64  `json:"exp"`
	Nonce  string `json:"nonce"`
	Return string `json:"return"`
}

func (p *Portal) external(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if p.m.cfg.ExternalURL == "" {
		http.NotFound(w, r)
		return
	}
	ip, ok := p.clientIP(r)
	if !ok {
		http.Error(w, "guest address unavailable", 403)
		return
	}
	nonce, err := randomToken()
	if err != nil {
		http.Error(w, "unavailable", 500)
		return
	}
	state := externalState{Guest: ip.String(), Exp: time.Now().Add(5 * time.Minute).Unix(), Nonce: nonce, Return: p.baseURL() + "/callback/external"}
	b, _ := json.Marshal(state)
	sig := mac(p.m.cfg.ExternalSecret, b)
	p.mu.Lock()
	for key, pending := range p.pendingExternal {
		if pending.Exp < time.Now().Unix() {
			delete(p.pendingExternal, key)
		}
	}
	if len(p.pendingExternal) >= 512 {
		p.mu.Unlock()
		http.Error(w, "too many pending sign-ins", 429)
		return
	}
	p.pendingExternal[nonce] = state
	p.mu.Unlock()
	u, err := url.Parse(p.m.cfg.ExternalURL)
	if err != nil {
		http.Error(w, "portal unavailable", 500)
		return
	}
	q := u.Query()
	q.Set("state", base64Raw(b))
	q.Set("signature", string(sig))
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}
func (p *Portal) externalCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	body, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		http.Error(w, "invalid callback", 400)
		return
	}
	if !validMAC(p.m.cfg.ExternalSecret, body, r.Header.Get("X-GameBond-Signature")) {
		http.Error(w, "invalid signature", 401)
		return
	}
	var v struct {
		Guest      string `json:"guest"`
		Exp        int64  `json:"exp"`
		Nonce      string `json:"nonce"`
		Authorized bool   `json:"authorized"`
		Duration   int    `json:"duration_seconds"`
	}
	if json.Unmarshal(body, &v) != nil || !v.Authorized || v.Exp < time.Now().Unix() || v.Exp > time.Now().Add(5*time.Minute).Unix() || len(v.Nonce) < 16 {
		http.Error(w, "invalid authorization", 400)
		return
	}
	guest, err := netip.ParseAddr(v.Guest)
	if err != nil || !guest.Is4() {
		http.Error(w, "invalid authorization", 400)
		return
	}
	p.mu.Lock()
	expected, exists := p.pendingExternal[v.Nonce]
	if exists {
		delete(p.pendingExternal, v.Nonce)
	}
	p.mu.Unlock()
	if !exists || expected.Guest != v.Guest || expected.Exp != v.Exp || expected.Exp < time.Now().Unix() || !p.markOnce(v.Nonce, time.Unix(v.Exp, 0)) {
		http.Error(w, "invalid or replayed authorization", 409)
		return
	}
	seconds := v.Duration
	if seconds <= 0 {
		seconds = p.m.cfg.SessionSeconds
	}
	if p.m.Grant(guest, time.Duration(seconds)*time.Second, time.Now(), "external") == "" {
		http.Error(w, "session could not be created", 503)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (p *Portal) payment(w http.ResponseWriter, r *http.Request) {
	if !p.form(w, r) {
		return
	}
	if p.m.cfg.PaymentEndpoint == "" {
		http.NotFound(w, r)
		return
	}
	ip, ok := p.clientIP(r)
	if !ok {
		http.Error(w, "guest address unavailable", 403)
		return
	}
	endpoint, err := url.Parse(p.m.cfg.PaymentEndpoint)
	if err != nil {
		http.Error(w, "payment unavailable", 503)
		return
	}
	body, _ := json.Marshal(map[string]any{"guest": ip.String(), "plan": p.m.cfg.PaymentPlan, "amount_minor": p.m.cfg.PaymentAmount, "currency": p.m.cfg.PaymentCurrency, "return_url": p.baseURL() + "/success", "webhook_url": p.baseURL() + "/callback/payment"})
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint.String(), strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.m.cfg.PaymentSecret)
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "payment service unavailable", 503)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "payment service unavailable", 503)
		return
	}
	var result struct {
		ID          string `json:"id"`
		CheckoutURL string `json:"checkout_url"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&result) != nil || result.ID == "" {
		http.Error(w, "invalid payment response", 502)
		return
	}
	u, err := url.Parse(result.CheckoutURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		http.Error(w, "invalid payment redirect", 502)
		return
	}
	if err = p.m.AllowBootstrapHost(u.Hostname()); err != nil {
		http.Error(w, "payment checkout host cannot be reached before authentication", 502)
		return
	}
	p.mu.Lock()
	for id, checkout := range p.pendingPayment {
		if time.Since(checkout.created) > 15*time.Minute {
			delete(p.pendingPayment, id)
		}
	}
	if len(p.pendingPayment) >= 256 {
		p.mu.Unlock()
		http.Error(w, "too many pending checkouts", 429)
		return
	}
	p.pendingPayment[result.ID] = pendingCheckout{guest: ip, created: time.Now()}
	p.mu.Unlock()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}
func (p *Portal) paymentCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid event", 400)
		return
	}
	if !validMAC(p.m.cfg.PaymentWebhook, body, r.Header.Get("X-GameBond-Signature")) {
		http.Error(w, "invalid signature", 401)
		return
	}
	var event struct {
		ID         string `json:"id"`
		CheckoutID string `json:"checkout_id"`
		Guest      string `json:"guest"`
		Status     string `json:"status"`
		Amount     int64  `json:"amount_minor"`
		Currency   string `json:"currency"`
		Plan       string `json:"plan"`
		Timestamp  int64  `json:"timestamp"`
		Duration   int    `json:"duration_seconds"`
	}
	if json.Unmarshal(body, &event) != nil || event.ID == "" || event.Guest == "" || event.Status != "paid" || event.Amount != p.m.cfg.PaymentAmount || !strings.EqualFold(event.Currency, p.m.cfg.PaymentCurrency) || event.Plan != p.m.cfg.PaymentPlan || time.Since(time.Unix(event.Timestamp, 0)) > 5*time.Minute || time.Unix(event.Timestamp, 0).After(time.Now().Add(time.Minute)) {
		http.Error(w, "payment event does not match checkout", 400)
		return
	}
	ip, err := netip.ParseAddr(event.Guest)
	if err != nil || !ip.Is4() || event.CheckoutID == "" || !p.markOnce("payment:"+event.ID, time.Now().Add(24*time.Hour)) {
		http.Error(w, "invalid or duplicate payment event", 409)
		return
	}
	p.mu.Lock()
	checkout, exists := p.pendingPayment[event.CheckoutID]
	if exists {
		delete(p.pendingPayment, event.CheckoutID)
	}
	p.mu.Unlock()
	if !exists || checkout.guest != ip || time.Since(checkout.created) > 15*time.Minute {
		http.Error(w, "payment does not match an active checkout", 409)
		return
	}
	d := event.Duration
	if d <= 0 {
		d = p.m.cfg.SessionSeconds
	}
	if p.m.Grant(ip, time.Duration(d)*time.Second, time.Now(), "payment") == "" {
		http.Error(w, "session could not be created", 503)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Portal) markOnce(key string, expires time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, v := range p.used {
		if !now.Before(v) {
			delete(p.used, k)
		}
	}
	if _, ok := p.used[key]; ok {
		return false
	}
	p.used[key] = expires
	return true
}
func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func base64Raw(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func VerifyPaymentSignature(secret string, body []byte, signature string) bool {
	return validMAC(secret, body, signature)
}
