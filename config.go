package main

import (
	"fmt"
	"gopkg.in/ini.v1"
)

// Config stores the application configuration
type Config struct {
	EmailHost     string `ini:"host"`
	EmailPort     int    `ini:"port"`
	EmailUsername string `ini:"username"`
	TelegramToken string `ini:"token"`
	TelegramUserID int64  `ini:"user_id"`
	CheckIntervalSeconds int `ini:"check_interval_seconds"`
}

// LoadConfig loads the configuration from the specified INI file
func LoadConfig(filePath string) (*Config, error) {
	var cfg Config
	err := ini.MapToWithMapper(&cfg, ini.TitleUnderscore, filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config file: %w", err)
	}

	// Manually map sections since ini.MapToWithMapper has limitations with sections
	// This is a common workaround.
	configFile, err := ini.Load(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config file: %w", err)
	}

	cfg.EmailHost = configFile.Section("email").Key("host").String()
	cfg.EmailPort, _ = configFile.Section("email").Key("port").Int()
	cfg.EmailUsername = configFile.Section("email").Key("username").String()
	cfg.TelegramToken = configFile.Section("telegram").Key("token").String()
	cfg.TelegramUserID, _ = configFile.Section("telegram").Key("user_id").Int64()
	cfg.CheckIntervalSeconds, _ = configFile.Section("app").Key("check_interval_seconds").Int()


	if cfg.EmailHost == "" || cfg.EmailPort == 0 || cfg.EmailUsername == "" || cfg.TelegramToken == "" || cfg.TelegramUserID == 0 {
		// CheckIntervalSeconds can have a default if not found, so not checking it here as "required" in the same way.
		// Or, ensure it has a default value if not present or invalid.
		return nil, fmt.Errorf("missing required configuration fields: EmailHost, EmailPort, EmailUsername, TelegramToken, TelegramUserID. CheckIntervalSeconds can have a default or be set in [app] section.")
	}
	
	if cfg.CheckIntervalSeconds <= 0 {
		log.Println("Warning: CheckIntervalSeconds not found in config or is invalid. Defaulting to 300 seconds.")
		cfg.CheckIntervalSeconds = 300 // Default to 5 minutes
	}


	return &cfg, nil
}
