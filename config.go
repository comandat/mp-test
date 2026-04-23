package main

import (
	"fmt"
	"os"
)

type Config struct {
	AgentMailAPIKey     string
	AgentMailInboxID    string
	AgentMailBaseURL    string
	DBPath              string
	Port                string
	NominatimUserAgent  string
	SyncIntervalSeconds int
}

func LoadConfig() (*Config, error) {
	c := &Config{
		AgentMailAPIKey:    os.Getenv("AGENTMAIL_API_KEY"),
		AgentMailInboxID:   os.Getenv("AGENTMAIL_INBOX_ID"),
		AgentMailBaseURL:   os.Getenv("AGENTMAIL_BASE_URL"),
		DBPath:             os.Getenv("DB_PATH"),
		Port:               os.Getenv("PORT"),
		NominatimUserAgent: os.Getenv("NOMINATIM_USER_AGENT"),
	}
	if c.AgentMailAPIKey == "" || c.AgentMailInboxID == "" {
		return nil, fmt.Errorf("AGENTMAIL_API_KEY and AGENTMAIL_INBOX_ID are required")
	}
	if c.AgentMailBaseURL == "" {
		c.AgentMailBaseURL = "https://api.agentmail.to"
	}
	if c.DBPath == "" {
		c.DBPath = "/data/app.db"
	}
	if c.Port == "" {
		c.Port = "8080"
	}
	if c.NominatimUserAgent == "" {
		c.NominatimUserAgent = "emag-tracker/1.0"
	}
	c.SyncIntervalSeconds = 120
	return c, nil
}
