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

func LoadConfig(fp string) (*Config, error) {

	// Load file from embed

	log.Println(au.Gray(12, "[CONFIG]"), au.Cyan("Loading configuration..."))
	cf, err := ini.Load(configContent)
	if err != nil {
		return nil, fmt.Errorf("failed to load config file: %w", err)
	}

	var cfg Config

	// Parse telegram section from embed

	cfg.TelegramToken = cf.Section("telegram").Key("token").String()
	cfg.TelegramUserId, _ = cf.Section("telegram").Key("user_id").Int64()
	cfg.TelegramChatId, _ = cf.Section("telegram").Key("chat_id").Int64()
	if cfg.TelegramToken == "" || (cfg.TelegramUserId == 0 && cfg.TelegramChatId == 0) {

		// Create new conf from template

		fn := fp + ".conf"
		_, err = os.Stat(fn)
		if err != nil || os.IsNotExist(err) {
			err := os.WriteFile(fn, configContent, 0600)
			if err != nil {
				return nil, fmt.Errorf("failed to write config to disk: %w", err)
			}
			return nil, fmt.Errorf("missing required configuration fields: TelegramToken and one of TelegramUserID or TelegramChatID")
		}

		// Or load from disk

		cf, err = ini.Load(fn)
		if err != nil {
			return nil, fmt.Errorf("failed to load config file: %w", err)
		}
		cfg.TelegramToken = cf.Section("telegram").Key("token").String()
		cfg.TelegramUserId, _ = cf.Section("telegram").Key("user_id").Int64()
		cfg.TelegramChatId, _ = cf.Section("telegram").Key("chat_id").Int64()
		if cfg.TelegramToken == "" || (cfg.TelegramUserId == 0 && cfg.TelegramChatId == 0) {
			return nil, fmt.Errorf("missing required configuration fields: TelegramToken and one of TelegramUserID or TelegramChatID")
		}
	}

	// Parse openai section (optional)

	ai, err := cf.GetSection("openai")
	if err == nil {
		if t, err := ai.GetKey("token"); err == nil {
			cfg.OpenAIToken = t.String()
		}
	}

	// Parse email section

	cfg.EmailImapPort, _ = cf.Section("email").Key("imap_port").Int()
	if cfg.EmailImapPort == 0 {
		cfg.EmailImapPort = 993
	}
	cfg.EmailSmtpPort, _ = cf.Section("email").Key("smtp_port").Int()
	if cfg.EmailSmtpPort == 0 {
		cfg.EmailSmtpPort = 587
	}

	emailUsername := cf.Section("email").Key("username").String()
	if emailUsername == "" {
		emailUsername = readUsername(cfg.TelegramUserId)
	} else {
		cfg.SetCred(emailUsername, "")
	}

	if host := cf.Section("email").Key("host").String(); len(host) > 0 {
		cfg.EmailImapHost = host
		cfg.EmailSmtpHost = host
	} else {
		cfg.EmailImapHost = cf.Section("email").Key("imap_host").String()
		cfg.EmailSmtpHost = cf.Section("email").Key("smtp_host").String()
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

func readUsername(uid int64) string {

	creds, _ := keyring.Get("email2Telegram", fmt.Sprint(uid))
	email, _ := parseCredString(creds)

	return email

}

func readCred(uid int64) (string, string) {

	creds, err := keyring.Get("email2Telegram", fmt.Sprint(uid))
	if err != nil {
		log.Printf(au.Gray(12, "[CONFIG]").String()+" "+au.Red("Failed to get credentials from keyring: %v").String(), err)
		decrypted, err := LoadAndDecrypt(fmt.Sprint(uid), fmt.Sprint(uid)+".key")
		if err != nil {
			log.Printf(au.Gray(12, "[CONFIG]").String()+" "+au.Red("Failed to get credentials from file: %v").String(), err)
		}
		fmt.Println(decrypted)
		return decrypted["email"], decrypted["password"]
	}

	return parseCredString(creds)

}

func (cfg *Config) updateHostIfNeeded(email string) {

	log.Printf(au.Gray(12, "[CONFIG]").String()+" "+au.Blue("Current IMAP host: %s").String(), cfg.EmailImapHost)
	if host, err := getHost(email); err == nil {
		log.Printf(au.Gray(12, "[CONFIG]").String()+" "+au.Blue("Extracted host from email: %s").String(), host)
		if len(cfg.EmailImapHost) == 0 {
			cfg.EmailImapHost = host
		}
		if len(cfg.EmailSmtpHost) == 0 {
			cfg.EmailSmtpHost = host
		}
	} else {
		log.Printf(au.Gray(12, "[CONFIG]").String()+" "+au.Red("Failed to extract host from email: %v").String(), err)
	}

}

// Get Set Cred

func (cfg *Config) GetCred() (string, string) {

	log.Println(au.Gray(12, "[CONFIG]"), au.Cyan("Retrieving credentials..."))
	email, password := readCred(cfg.TelegramUserId)
	cfg.updateHostIfNeeded(email)

	return email, password

}

func (cfg *Config) SetCred(email string, password string) {

	log.Println(au.Gray(12, "[CONFIG]"), au.Cyan("Storing credentials..."))
	err := keyring.Set("email2Telegram", fmt.Sprint(cfg.TelegramUserId), createCredString(email, password))
	if err != nil {
		log.Printf(au.Gray(12, "[CONFIG]").String()+" "+au.Red("Failed to store credentials in keyring: %v").String(), err)
		creds := map[string]string{"email": email, "password": password}
		err := EncryptAndSave(fmt.Sprint(cfg.TelegramUserId), fmt.Sprint(cfg.TelegramUserId)+".key", creds)
		if err != nil {
			log.Printf(au.Gray(12, "[CONFIG]").String()+" "+au.Red("Failed to store credentials in file: %v").String(), err)
		} else {
			log.Println(au.Gray(12, "[CONFIG]"), au.Green("Credentials stored securely in file: "+fmt.Sprint(cfg.TelegramUserId)+".key"))
		}
	} else {
		log.Println(au.Gray(12, "[CONFIG]"), au.Green("Credentials stored securely in keyring"))
	}
	cfg.updateHostIfNeeded(email)

}
