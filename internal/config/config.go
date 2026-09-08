package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Token                                                                                                string
	GroupChatID                                                                                          int64
	AdminTelegramUserID                                                                                  int64
	DiscoveryMode                                                                                        bool
	InfoChatIDs                                                                                          map[int64]bool
	GroupName, Timezone, DBPath, BaseURL, ExternalToken, AdminPassword, BootstrapAdminUsername, HTTPAddr string
	Retention                                                                                            time.Duration
	AIBaseURL, AIKey, AITextModel, AIVisionModel, AISTTModel                                             string
}

func Load() (Config, error) {
	c := Config{Token: os.Getenv("TELEGRAM_BOT_TOKEN"), DiscoveryMode: strings.EqualFold(os.Getenv("TELEGRAM_DISCOVERY_MODE"), "true"), GroupName: value("GROUP_NAME", "411 группа"), Timezone: value("GROUP_TIMEZONE", "Europe/Moscow"), DBPath: value("DATABASE_PATH", "data/app.db"), BaseURL: strings.TrimRight(value("BASE_URL", "http://localhost:8080"), "/"), ExternalToken: os.Getenv("EXTERNAL_API_TOKEN"), AdminPassword: os.Getenv("ADMIN_PASSWORD"), BootstrapAdminUsername: strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value("BOOTSTRAP_ADMIN_USERNAME", "yaroslavsavateykin"))), "@"), HTTPAddr: value("HTTP_ADDR", ":8080"), Retention: 48 * time.Hour, InfoChatIDs: map[int64]bool{}, AIBaseURL: os.Getenv("AI_BASE_URL"), AIKey: os.Getenv("AI_API_KEY"), AITextModel: os.Getenv("AI_TEXT_MODEL"), AIVisionModel: os.Getenv("AI_VISION_MODEL"), AISTTModel: os.Getenv("AI_STT_MODEL")}
	var err error
	if v := os.Getenv("ADMIN_TELEGRAM_USER_ID"); v != "" {
		c.AdminTelegramUserID, err = strconv.ParseInt(v, 10, 64)
		if err != nil || c.AdminTelegramUserID <= 0 {
			return c, fmt.Errorf("invalid ADMIN_TELEGRAM_USER_ID")
		}
	}
	if s := os.Getenv("TELEGRAM_GROUP_CHAT_ID"); s != "" {
		c.GroupChatID, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			return c, fmt.Errorf("TELEGRAM_GROUP_CHAT_ID: %w", err)
		}
	}
	for _, s := range strings.Split(os.Getenv("TELEGRAM_INFO_CHAT_IDS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			id, e := strconv.ParseInt(s, 10, 64)
			if e != nil {
				return c, fmt.Errorf("TELEGRAM_INFO_CHAT_IDS: %w", e)
			}
			c.InfoChatIDs[id] = true
		}
	}
	if s := os.Getenv("RAW_MESSAGE_RETENTION_HOURS"); s != "" {
		h, e := strconv.Atoi(s)
		if e != nil || h < 1 {
			return c, fmt.Errorf("invalid RAW_MESSAGE_RETENTION_HOURS")
		}
		c.Retention = time.Duration(h) * time.Hour
	}
	if _, e := time.LoadLocation(c.Timezone); e != nil {
		return c, fmt.Errorf("GROUP_TIMEZONE: %w", e)
	}
	return c, nil
}
func value(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
