package main

import (
	"fmt"
	"log"

	"gopkg.in/ini.v1"
)

type Config struct {
	EmailImapHost        string `ini:"imap_host"`
	EmailImapPort        int    `ini:"imap_port"`
	EmailSmtpHost        string `ini:"smtp_host"`
	EmailSmtpPort        int    `ini:"smtp_port"`
	EmailUsername        string `ini:"username"`
	TelegramToken        string `ini:"token"`
	TelegramUserID       int64  `ini:"user_id"`
	CheckIntervalSeconds int    `ini:"check_interval_seconds"`
}

func LoadConfig(filePath string) (*Config, error) {

	// Load file

	configFile, err := ini.Load(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config file: %w", err)
	}

	// Create Config

	var cfg Config
	err = ini.MapToWithMapper(&cfg, ini.TitleUnderscore, filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config file: %w", err)
	}

	// Parse Config

	if host := configFile.Section("email").Key("host").String(); len(host) > 0 {
		cfg.EmailImapHost = host
		cfg.EmailSmtpHost = host
	} else {
		cfg.EmailImapHost = configFile.Section("email").Key("imap_host").String()
		cfg.EmailSmtpHost = configFile.Section("email").Key("smtp_host").String()
	}
	cfg.EmailImapPort, _ = configFile.Section("email").Key("imap_port").Int()
	cfg.EmailSmtpPort, _ = configFile.Section("email").Key("smtp_port").Int()

	cfg.EmailUsername = configFile.Section("email").Key("username").String()
	cfg.TelegramToken = configFile.Section("telegram").Key("token").String()
	cfg.TelegramUserID, _ = configFile.Section("telegram").Key("user_id").Int64()
	cfg.CheckIntervalSeconds, _ = configFile.Section("app").Key("check_interval_seconds").Int()
	if cfg.EmailImapPort == 0 {
		cfg.EmailImapPort = 993
	}
	if cfg.EmailSmtpPort == 0 {
		cfg.EmailSmtpPort = 587
	}
	if cfg.EmailImapHost == "" || cfg.EmailSmtpHost == "" || cfg.EmailUsername == "" || cfg.TelegramToken == "" || cfg.TelegramUserID == 0 {
		return nil, fmt.Errorf("missing required configuration fields: EmailHost, EmailPort, EmailUsername, TelegramToken, TelegramUserID. CheckIntervalSeconds can have a default or be set in [app] section")
	}

	// Retest Config file

	if cfg.CheckIntervalSeconds <= 0 {
		log.Println("Warning: CheckIntervalSeconds not found in config or is invalid. Defaulting to 300 seconds.")
		cfg.CheckIntervalSeconds = 300 // Default to 5 minutes
	}

	return &cfg, nil
}
