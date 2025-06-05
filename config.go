package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
	"gopkg.in/ini.v1"

	_ "embed"
)

//go:embed config.ini
var configContent []byte

type Config struct {
	EmailImapHost        string `ini:"imap_host"`
	EmailImapPort        int    `ini:"imap_port"`
	EmailSmtpHost        string `ini:"smtp_host"`
	EmailSmtpPort        int    `ini:"smtp_port"`
	TelegramToken        string `ini:"token"`
	TelegramUserId       int64  `ini:"user_id"`
	CheckIntervalSeconds int    `ini:"check_interval_seconds"`
}

func LoadConfig(filePath string) (*Config, error) {

	// Load file

	configFile, err := ini.Load(configContent)
	if err != nil {
		return nil, fmt.Errorf("failed to load config file: %w", err)
	}

	var cfg Config

	// Parse telegram section

	cfg.TelegramToken = configFile.Section("telegram").Key("token").String()
	cfg.TelegramUserId, _ = configFile.Section("telegram").Key("user_id").Int64()
	if cfg.TelegramToken == "" || cfg.TelegramUserId == 0 {
		return nil, fmt.Errorf("missing required configuration fields: TelegramToken, TelegramUserID. CheckIntervalSeconds can have a default or be set in [app] section")
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
		decrypted, err := loadAndDecrypt(userId, fmt.Sprint(userId)+".key")
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
		err := encryptAndSave(cfg.TelegramUserId, fmt.Sprint(cfg.TelegramUserId)+".key", creds)
		if err != nil {
			log.Printf("failed get cred from file: %v", err)
		}
	}
	cfg.updateHostIfNeeded(email)
}

// If keyring not working

func deriveKey(userID int64) []byte {
	sum := sha256.Sum256([]byte(fmt.Sprint(userID)))
	return sum[:]
}

func encryptAndSave(userID int64, filepath string, data map[string]string) error {
	key := deriveKey(userID)

	plaintext, err := json.Marshal(data)
	if err != nil {
		return err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return os.WriteFile(filepath, ciphertext, 0600)
}

func loadAndDecrypt(userID int64, filepath string) (map[string]string, error) {
	key := deriveKey(userID)

	ciphertext, err := os.ReadFile(filepath)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	nonce, data := ciphertext[:nonceSize], ciphertext[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]string
	err = json.Unmarshal(plaintext, &result)
	return result, err
}
