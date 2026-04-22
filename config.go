package main

import (
	"fmt"
	"os"
)

type Config struct {
	ProtonUsername        string
	ProtonPassword        string
	ProtonMailboxPassword string
	ProtonTOTPSecret      string
	ProtonAppVersion      string
	ProtonDebug           bool
	DBPath                string
	Port                  string
	NominatimUserAgent    string
	SyncIntervalSeconds   int
}

func LoadConfig() (*Config, error) {
	c := &Config{
		ProtonUsername:        os.Getenv("PROTON_USERNAME"),
		ProtonPassword:        os.Getenv("PROTON_PASSWORD"),
		ProtonMailboxPassword: os.Getenv("PROTON_MAILBOX_PASSWORD"),
		ProtonTOTPSecret:      os.Getenv("PROTON_TOTP_SECRET"),
		ProtonAppVersion:      os.Getenv("PROTON_APP_VERSION"),
		ProtonDebug:           os.Getenv("PROTON_DEBUG") == "1",
		DBPath:                os.Getenv("DB_PATH"),
		Port:                  os.Getenv("PORT"),
		NominatimUserAgent:    os.Getenv("NOMINATIM_USER_AGENT"),
	}
	if c.ProtonUsername == "" || c.ProtonPassword == "" {
		return nil, fmt.Errorf("PROTON_USERNAME and PROTON_PASSWORD are required")
	}
	if c.ProtonMailboxPassword == "" {
		c.ProtonMailboxPassword = c.ProtonPassword
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
