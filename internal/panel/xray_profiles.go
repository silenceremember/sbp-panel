package panel

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
)

func withXrayFingerprint(credential, fingerprint string) (string, error) {
	switch fingerprint {
	case "chrome", "firefox", "safari", "ios", "android", "edge", "random", "randomized":
	default:
		return "", errors.New("unsupported fingerprint")
	}
	link, err := url.Parse(credential)
	if err != nil || link.Scheme != "vless" {
		return "", errors.New("invalid VLESS profile")
	}
	query := link.Query()
	query.Set("fp", fingerprint)
	link.RawQuery = query.Encode()
	return link.String(), nil
}

// Amnezia's QR importer recognizes native Xray JSON, whereas its URI
// importer currently drops XHTTP path. Keep VLESS URIs for other clients.
func xrayNativeProfile(credential string) (string, error) {
	link, err := url.Parse(credential)
	if err != nil || link.Scheme != "vless" || link.User == nil {
		return "", errors.New("invalid VLESS profile")
	}
	port, err := strconv.Atoi(link.Port())
	if err != nil {
		return "", err
	}
	q := link.Query()
	user := map[string]any{"id": link.User.Username(), "encryption": "none"}
	if flow := q.Get("flow"); flow != "" {
		user["flow"] = flow
	}
	stream := map[string]any{"network": q.Get("type"), "security": "reality", "realitySettings": map[string]any{
		"serverName": q.Get("sni"), "fingerprint": q.Get("fp"), "publicKey": q.Get("pbk"), "shortId": q.Get("sid")}}
	if q.Get("type") == "xhttp" {
		stream["xhttpSettings"] = map[string]any{"path": q.Get("path"), "mode": "auto"}
	}
	config := map[string]any{
		"remarks":   link.Fragment,
		"inbounds":  []any{map[string]any{"listen": "127.0.0.1", "port": 10808, "protocol": "socks", "settings": map[string]any{"udp": true}}},
		"outbounds": []any{map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": link.Hostname(), "port": port, "users": []any{user}}}}, "streamSettings": stream}},
	}
	body, err := json.Marshal(config)
	return string(body), err
}
