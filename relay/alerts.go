package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// alertConfig holds the fault-alert delivery settings (DB-backed, dashboard-edited,
// with env defaults). An alert fan-outs to every configured channel.
type alertConfig struct {
	Enabled       bool   `json:"enabled"`
	TelegramToken string `json:"telegram_token"`
	TelegramChat  string `json:"telegram_chat"`
	WebhookURL    string `json:"webhook_url"`
}

// hasChannel reports whether at least one delivery channel is fully configured.
func (c alertConfig) hasChannel() bool {
	return (c.TelegramToken != "" && c.TelegramChat != "") || c.WebhookURL != ""
}

// active reports whether alerts should actually be sent.
func (c alertConfig) active() bool { return c.Enabled && c.hasChannel() }

var alertClient = &http.Client{Timeout: 10 * time.Second}

// sendAlert delivers a message to every configured channel, best-effort and
// asynchronously (never blocks the caller / monitor). title is a short headline,
// body the detail line.
func sendAlert(c alertConfig, title, body string) {
	if !c.hasChannel() {
		return
	}
	text := title
	if body != "" {
		text += "\n" + body
	}
	if c.TelegramToken != "" && c.TelegramChat != "" {
		go sendTelegram(c.TelegramToken, c.TelegramChat, text)
	}
	if c.WebhookURL != "" {
		go sendWebhook(c.WebhookURL, title, body, text)
	}
}

func sendTelegram(token, chat, text string) {
	api := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	form := url.Values{"chat_id": {chat}, "text": {text}}
	resp, err := alertClient.PostForm(api, form)
	if err != nil {
		log.Printf("[alert] telegram send error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[alert] telegram returned %d", resp.StatusCode)
	}
}

func sendWebhook(rawURL, title, body, text string) {
	// Guard against SSRF: only allow http(s) to a real host, not loopback/metadata.
	if !webhookURLAllowed(rawURL) {
		log.Printf("[alert] webhook URL not allowed: %s", rawURL)
		return
	}
	payload, _ := json.Marshal(map[string]string{
		"title": title, "body": body, "text": text,
		"ts": time.Now().UTC().Format(time.RFC3339),
	})
	resp, err := alertClient.Post(rawURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("[alert] webhook send error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("[alert] webhook returned %d", resp.StatusCode)
	}
}

// webhookURLAllowed permits only http(s) URLs to a non-empty, non-local host.
func webhookURLAllowed(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	return !isBlockedRTSPHost(host) // blocks loopback + cloud-metadata; LAN/public OK
}

// loadAlertConfig reads the alert settings from the DB (falling back to env vars
// per field): RELAY_TELEGRAM_TOKEN / RELAY_TELEGRAM_CHAT / RELAY_ALERT_WEBHOOK /
// RELAY_ALERTS_ENABLED.
func loadAlertConfig(store *agentStore) alertConfig {
	get := func(key, env string) string {
		if store != nil {
			if v, err := store.getSetting(key); err == nil && v != "" {
				return v
			}
		}
		return os.Getenv(env)
	}
	c := alertConfig{
		TelegramToken: get(settingAlertTGToken, "RELAY_TELEGRAM_TOKEN"),
		TelegramChat:  get(settingAlertTGChat, "RELAY_TELEGRAM_CHAT"),
		WebhookURL:    get(settingAlertWebhook, "RELAY_ALERT_WEBHOOK"),
	}
	en := get(settingAlertEnabled, "RELAY_ALERTS_ENABLED")
	c.Enabled = en == "1" || strings.EqualFold(en, "true")
	return c
}

// alertMonitor runs for the relay's lifetime and fires fault alerts when an agent
// (지점) goes offline for longer than the grace period, and again on recovery.
// It only tracks agents that have connected at least once, and de-dupes so each
// transition alerts exactly once.
func (h *Hub) alertMonitor() {
	const interval = 20 * time.Second
	const offlineGrace = 30 * time.Second
	down := map[string]bool{} // session id -> already alerted as down
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
		}
		cfg := h.getAlertConfig()
		if !cfg.active() {
			continue
		}
		now := time.Now()
		for _, s := range h.allSessions() {
			if s.connectedAt.Load() == 0 {
				continue // never connected — nothing to alert about
			}
			label := s.name
			if label == "" {
				label = s.id
			}
			if s.online() {
				if down[s.id] {
					delete(down, s.id)
					sendAlert(cfg, "🟢 "+label+" 복구", "에이전트가 다시 온라인입니다.")
				}
				continue
			}
			last := s.lastPublishAt.Load()
			if last == 0 || down[s.id] {
				continue
			}
			if off := now.Sub(time.UnixMilli(last)); off >= offlineGrace {
				down[s.id] = true
				sendAlert(cfg, "🔴 "+label+" 오프라인", fmt.Sprintf("에이전트 응답 없음 (%s).", off.Round(time.Second)))
			}
		}
	}
}
