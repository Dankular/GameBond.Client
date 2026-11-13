package dashboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gamebond.local/nativeedge/atomicio"
	"gamebond.local/nativeedge/hotspot"
)

func redactHotspot(raw any) map[string]any {
	out, _ := raw.(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	copy := map[string]any{}
	for k, v := range out {
		if k != "password_hash" && k != "external_secret" && k != "payment_secret" && k != "payment_webhook_secret" && k != "vouchers" {
			copy[k] = v
		}
	}
	if vouchers, ok := out["vouchers"].([]any); ok {
		copy["voucher_count"] = len(vouchers)
	}
	copy["has_password"] = out["password_hash"] != nil && out["password_hash"] != ""
	copy["external_configured"] = out["external_secret"] != nil && out["external_secret"] != ""
	copy["payment_configured"] = out["payment_secret"] != nil && out["payment_secret"] != ""
	return copy
}

func decodeHotspot(raw any) (hotspot.Config, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return hotspot.Config{}, err
	}
	var c hotspot.Config
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (s *Server) hotspotAPI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/hotspot/sessions" {
		s.hotspotSessionAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/hotspot/vouchers") {
		s.hotspotVouchers(w, r)
		return
	}
	s.configSectionWithConfig(w, r, func(cfg map[string]any) map[string]any {
		return map[string]any{"hotspot": redactHotspot(cfg["hotspot"])}
	}, func(cfg map[string]any, body []byte) (map[string]any, error) {
		if r.Method != http.MethodPut {
			return nil, errors.New("GET or PUT only")
		}
		incoming, err := object(body)
		if err != nil {
			return nil, err
		}
		previous, _ := cfg["hotspot"].(map[string]any)
		if previous["vouchers"] != nil {
			incoming["vouchers"] = previous["vouchers"]
		}
		for _, secret := range []string{"external_secret", "payment_secret", "payment_webhook_secret"} {
			if incoming[secret] == nil || incoming[secret] == "" {
				if old := previous[secret]; old != nil {
					incoming[secret] = old
				}
			}
		}
		if password, ok := incoming["password"].(string); ok {
			delete(incoming, "password")
			if password != "" {
				hash, e := hotspot.HashPassword(password)
				if e != nil {
					return nil, e
				}
				incoming["password_hash"] = hash
			} else if previous["password_hash"] != nil {
				incoming["password_hash"] = previous["password_hash"]
			}
		} else if previous["password_hash"] != nil {
			incoming["password_hash"] = previous["password_hash"]
		}
		for k := range incoming {
			if k == "has_password" || k == "external_configured" || k == "payment_configured" {
				delete(incoming, k)
			}
		}
		c, err := decodeHotspot(incoming)
		if err != nil {
			return nil, err
		}
		if c.Enabled {
			configured := map[string]bool{}
			if ds, ok := cfg["lan_devices"].([]any); ok {
				for _, v := range ds {
					if d, ok := v.(map[string]any); ok {
						if name, ok := d["name"].(string); ok {
							configured[name] = true
						}
					}
				}
			}
			for _, name := range c.Devices {
				if !configured[name] {
					return nil, errors.New("guest devices must already exist in Connected devices")
				}
			}
		}
		return map[string]any{"hotspot": incoming}, nil
	})
}

func (s *Server) hotspotSessionAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		reply(w, 405, map[string]any{"error": "POST only"})
		return
	}
	if s.o.StatusPath == "" {
		reply(w, 503, map[string]any{"error": "runtime status path unavailable"})
		return
	}
	var in struct {
		Guest string `json:"guest"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in) != nil {
		reply(w, 400, map[string]any{"error": "guest IPv4 address required"})
		return
	}
	ip, err := netip.ParseAddr(in.Guest)
	if err != nil || !ip.Is4() {
		reply(w, 400, map[string]any{"error": "guest IPv4 address required"})
		return
	}
	path := filepath.Join(filepath.Dir(s.o.StatusPath), "hotspot-control.json")
	s.mu.Lock()
	command := struct {
		Revoke []string `json:"revoke"`
	}{}
	if old, readErr := os.ReadFile(path); readErr == nil {
		_ = json.Unmarshal(old, &command)
	}
	if len(command.Revoke) >= 64 {
		s.mu.Unlock()
		reply(w, 429, map[string]any{"error": "too many pending revocations"})
		return
	}
	command.Revoke = append(command.Revoke, ip.String())
	b, _ := json.Marshal(command)
	if err = atomicio.WriteFileAtomic(path, b, 0600); err != nil {
		s.mu.Unlock()
		reply(w, 500, map[string]any{"error": err.Error()})
		return
	}
	s.mu.Unlock()
	reply(w, 200, map[string]any{"ok": true, "guest": ip.String()})
}

func (s *Server) hotspotVouchers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := readConfig(s.o.ConfigPath)
	if err != nil {
		reply(w, 500, map[string]any{"error": err.Error()})
		return
	}
	h, ok := cfg["hotspot"].(map[string]any)
	if !ok {
		reply(w, 409, map[string]any{"error": "save hotspot settings before managing vouchers"})
		return
	}
	var vouchers []any
	vouchers, _ = h["vouchers"].([]any)
	switch r.Method {
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
		if err != nil {
			reply(w, 400, map[string]any{"error": "invalid request"})
			return
		}
		var in struct {
			Uses     int    `json:"uses"`
			Duration int    `json:"duration_seconds"`
			Expires  string `json:"expires_at"`
		}
		if json.Unmarshal(body, &in) != nil {
			reply(w, 400, map[string]any{"error": "invalid request"})
			return
		}
		c, err := decodeHotspot(h)
		if err != nil {
			reply(w, 422, map[string]any{"error": err.Error()})
			return
		}
		var expiry time.Time
		if in.Expires != "" {
			expiry, err = time.Parse(time.RFC3339, in.Expires)
			if err != nil {
				reply(w, 422, map[string]any{"error": "expires_at must be RFC3339"})
				return
			}
		}
		code, spec, err := hotspot.GenerateVoucher(in.Uses, expiry, in.Duration)
		if err != nil {
			reply(w, 422, map[string]any{"error": err.Error()})
			return
		}
		if in.Duration > c.SessionSeconds {
			reply(w, 422, map[string]any{"error": "voucher duration exceeds hotspot session limit"})
			return
		}
		vouchers = append(vouchers, map[string]any{"hash": spec.Hash, "uses": spec.Uses, "duration_seconds": spec.Duration, "expires_at": spec.ExpiresAt})
		h["vouchers"] = vouchers
		cfg["hotspot"] = h
		if err = writeConfig(s.o.ConfigPath, cfg); err != nil {
			reply(w, 500, map[string]any{"error": err.Error()})
			return
		}
		reply(w, 200, map[string]any{"ok": true, "voucher": code, "restart_required": true})
	case http.MethodDelete:
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
		if err != nil {
			reply(w, 400, map[string]any{"error": "invalid request"})
			return
		}
		var in struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(body, &in) != nil || in.Code == "" {
			reply(w, 400, map[string]any{"error": "voucher code required"})
			return
		}
		digest := hotspot.VoucherDigest(in.Code)
		kept := make([]any, 0, len(vouchers))
		found := false
		for _, entry := range vouchers {
			if v, ok := entry.(map[string]any); ok && v["hash"] == digest {
				found = true
				continue
			}
			kept = append(kept, entry)
		}
		if !found {
			reply(w, 404, map[string]any{"error": "voucher not found"})
			return
		}
		h["vouchers"] = kept
		cfg["hotspot"] = h
		if err = writeConfig(s.o.ConfigPath, cfg); err != nil {
			reply(w, 500, map[string]any{"error": err.Error()})
			return
		}
		reply(w, 200, map[string]any{"ok": true, "restart_required": true})
	default:
		reply(w, 405, map[string]any{"error": "POST or DELETE only"})
	}
}
