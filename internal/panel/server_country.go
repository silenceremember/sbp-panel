package panel

import (
	"io"
	"net/http"
	"strings"
	"time"
)

// The lookup runs once on startup; a saved country is also a manual override.
func (s *server) detectServerCountry() {
	if country, _ := s.db.Setting("server_country"); country != "" {
		return
	}
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Get("https://ipapi.co/country_name/")
	if err != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 81))
	country := strings.TrimSpace(string(body))
	if err != nil || country == "" || len(country) > 80 || strings.ContainsAny(country, "\r\n<>{}") {
		return
	}
	// A user may have saved an override while the request was in flight.
	_, _ = s.db.DB.Exec(`INSERT INTO settings(key,value) VALUES('server_country',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE settings.value=''`, country)
}
