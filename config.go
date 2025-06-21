package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
	"gopkg.in/ini.v1"

	_ "embed"
)

//go:embed email2telegram.conf
var configContent []byte

type Config struct {
	EmailImapHost        string `ini:"imap_host"`
	EmailImapPort        int    `ini:"imap_port"`
	EmailSmtpHost        string `ini:"smtp_host"`
	EmailSmtpPort        int    `ini:"smtp_port"`
	TelegramToken        string `ini:"token"`
	TelegramUserId       int64  `ini:"user_id"`
	TelegramChatId       int64  `ini:"chat_id"`
	OpenAIToken          string `ini:"token"`
	CheckIntervalSeconds int    `ini:"check_interval_seconds"`
}

func LoadConfig(filePath string) (*Config, error) {

	// Load file from embed

	configFile, err := ini.Load(configContent)
	if err != nil {
		return nil, fmt.Errorf("failed to load config file: %w", err)
	}

	var cfg Config

	// Parse telegram section from embed

	cfg.TelegramToken = configFile.Section("telegram").Key("token").String()
	cfg.TelegramUserId, _ = configFile.Section("telegram").Key("user_id").Int64()
	cfg.TelegramChatId, _ = configFile.Section("telegram").Key("chat_id").Int64()
	if cfg.TelegramToken == "" || (cfg.TelegramUserId == 0 && cfg.TelegramChatId == 0) {

		// Create new conf from template

		configFilename := filePath + ".conf"
		_, err = os.Stat(configFilename)
		if err != nil || os.IsNotExist(err) {
			err := os.WriteFile(configFilename, configContent, 0600)
			if err != nil {
				return nil, fmt.Errorf("failed to write config to disk: %w", err)
			}
			return nil, fmt.Errorf("missing required configuration fields: TelegramToken and one of TelegramUserID or TelegramChatID")
		}

		// Or load from disk

		configFile, err = ini.Load(configFilename)
		if err != nil {
			return nil, fmt.Errorf("failed to load config file: %w", err)
		}
		cfg.TelegramToken = configFile.Section("telegram").Key("token").String()
		cfg.TelegramUserId, _ = configFile.Section("telegram").Key("user_id").Int64()
		cfg.TelegramChatId, _ = configFile.Section("telegram").Key("chat_id").Int64()
		if cfg.TelegramToken == "" || (cfg.TelegramUserId == 0 && cfg.TelegramChatId == 0) {
			return nil, fmt.Errorf("missing required configuration fields: TelegramToken and one of TelegramUserID or TelegramChatID")
		}
	}

	// Parse openai section (optional)

	openAISection, err := configFile.GetSection("openai")
	if err == nil {
		if tokenKey, keyErr := openAISection.GetKey("token"); keyErr == nil {
			cfg.OpenAIToken = tokenKey.String()
		}
	} else {
		configFilename := filePath
		if !strings.HasSuffix(configFilename, ".conf") {
			configFilename += ".conf"
		}

		if _, statErr := os.Stat(configFilename); statErr == nil {
			diskConfigFile, loadErr := ini.Load(configFilename)
			if loadErr == nil {
				openAISectionFromDisk, sectionErr := diskConfigFile.GetSection("openai")
				if sectionErr == nil {
					if tokenKey, keyErr := openAISectionFromDisk.GetKey("token"); keyErr == nil {
						cfg.OpenAIToken = tokenKey.String()
					}
				}
			}
		}
	}

	// Parse email section

	cfg.EmailImapPort, _ = configFile.Section("email").Key("imap_port").Int()
	if cfg.EmailImapPort == 0 {
		cfg.EmailImapPort = 993
	}
	cfg.EmailSmtpPort, _ = configFile.Section("email").Key("smtp_port").Int()
	if cfg.EmailSmtpPort == 0 {
		cfg.EmailSmtpPort = 587
	}

	emailUsername := configFile.Section("email").Key("username").String()
	if emailUsername == "" {
		emailUsername = readUsername(cfg.TelegramUserId)
	} else {
		cfg.SetCred(emailUsername, "")
	}

	if host := configFile.Section("email").Key("host").String(); len(host) > 0 {
		cfg.EmailImapHost = host
		cfg.EmailSmtpHost = host
	} else {
		cfg.EmailImapHost = configFile.Section("email").Key("imap_host").String()
		cfg.EmailSmtpHost = configFile.Section("email").Key("smtp_host").String()
	}
	if host, err := getHost(emailUsername); err == nil {
		if len(cfg.EmailImapHost) == 0 {
			cfg.EmailImapHost = host
		}
		if len(cfg.EmailSmtpHost) == 0 {
			cfg.EmailSmtpHost = host
		}
	}

	return &cfg, nil
}

func getHost(email string) (string, error) {

	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return "", fmt.Errorf("defect email username")
	}

	return parts[1], nil

}

// Keychain

func createCredString(email, password string) string {

	return fmt.Sprintf("%s:%s", email, password)

}

func parseCredString(cred string) (string, string) {

	parts := strings.SplitN(cred, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}

	return parts[0], parts[1]

}

func readUsername(userId int64) string {

	creds, _ := keyring.Get("email2Telegram", fmt.Sprint(userId))
	email, _ := parseCredString(creds)

	return email

}

func readCred(userId int64) (string, string) {

	creds, err := keyring.Get("email2Telegram", fmt.Sprint(userId))
	if err != nil {
		log.Printf("failed get cred: %v", err)
		decrypted, err := LoadAndDecrypt(fmt.Sprint(userId), fmt.Sprint(userId)+".key")
		if err != nil {
			log.Printf("failed get cred from file: %v", err)
		}
		fmt.Println(decrypted)
		return decrypted["email"], decrypted["password"]
	}

	return parseCredString(creds)

}

func (cfg *Config) updateHostIfNeeded(email string) {

	log.Printf("imap host: %s", cfg.EmailImapHost)
	if host, err := getHost(email); err == nil {
		log.Printf("new host: %s", host)
		if len(cfg.EmailImapHost) == 0 {
			cfg.EmailImapHost = host
		}
		if len(cfg.EmailSmtpHost) == 0 {
			cfg.EmailSmtpHost = host
		}
	} else {
		log.Printf("failed get host from cred: %v", err)
	}

}

// Get Set Cred

func (cfg *Config) GetCred() (string, string) {

	email, password := readCred(cfg.TelegramUserId)
	cfg.updateHostIfNeeded(email)

	return email, password

}

func (cfg *Config) SetCred(email string, password string) {

	err := keyring.Set("email2Telegram", fmt.Sprint(cfg.TelegramUserId), createCredString(email, password))
	if err != nil {
		log.Printf("failed set cred: %v", err)
		creds := map[string]string{"email": email, "password": password}
		err := EncryptAndSave(fmt.Sprint(cfg.TelegramUserId), fmt.Sprint(cfg.TelegramUserId)+".key", creds)
		if err != nil {
			log.Printf("failed get cred from file: %v", err)
		}
	}
	cfg.updateHostIfNeeded(email)

}
