package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func loadEMLFile(filename string) ([]byte, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read EML file: %w", err)
	}

	return content, nil
}

func testEMLProcessing(emlPath string) {
	log.Println("--- Testing EML file processing ---")
	log.Printf("Loading EML file: %s", emlPath)

	msg, err := loadEMLFile(emlPath)
	if err != nil {
		log.Printf("Error loading EML file: %v", err)
		return
	}

	parsedData, err := ParseEmail(msg)
	if err != nil {
		log.Printf("Error parsing email: %v", err)
		return
	}

	log.Println("Email parsed successfully:")
	log.Printf("- Subject: %s", parsedData.Subject)
	log.Printf("- Has HTML: %v", parsedData.HasHTML)
	log.Printf("- Has Text: %v", parsedData.HasText)
	log.Printf("%s", parsedData.TextBody)
	log.Printf("- Attachments: %d", len(parsedData.Attachments))

	if parsedData.PDFBody != nil {
		pdfPath := filepath.Join(".", "output.pdf")
		err = os.WriteFile(pdfPath, parsedData.PDFBody, 0644)
		if err != nil {
			log.Printf("Failed to save PDF: %v", err)
		} else {
			log.Printf("PDF saved to: %s (%d bytes)", pdfPath, len(parsedData.PDFBody))
		}
	} else if parsedData.HTMLBody != "" {
		log.Println("No PDF generated but HTML content is available")
	} else {
		log.Println("No HTML content available for PDF generation")
	}

	log.Println("--- EML test completed ---")
}

func createSimpleHTMLMockMessage() *mail.Message {
	htmlBody := `
	<!DOCTYPE html>
	<html>
	<head><title>Test PDF</title></head>
	<body>
		<h1>Hello, Rod!</h1>
		<p>This is a test PDF generation using go-rod.</p>
		<p style="color:blue;">Some blue text.</p>
	</body>
	</html>`

	encodedHTML := base64.StdEncoding.EncodeToString([]byte(htmlBody))

	emlContent := fmt.Sprintf(`Subject: Rod PDF Test
From: test@example.com
To: bot@example.com
Content-Type: text/html; charset="utf-8"
Content-Transfer-Encoding: base64

%s`, encodedHTML)

	msg, err := mail.ReadMessage(strings.NewReader(emlContent))
	if err != nil {
		log.Printf("Error creating simple HTML mock message: %v", err)
		return &mail.Message{
			Header: map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			Body:   strings.NewReader("<h1>Fallback HTML</h1>"),
		}
	}
	return msg
}

func main() {
	log.Println("Starting Email Processor...")

	// Проверяем наличие файла example.eml
	emlPath := "example.eml"
	if _, err := os.Stat(emlPath); err == nil {
		testEMLProcessing(emlPath)
	}

	// Остальная часть main (конфигурация, IMAP и т.д.)
	cfg, err := LoadConfig("config.ini")
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("Config file not found, creating template...")
			createDefaultConfig()
			return
		}
		log.Fatalf("Failed to load config: %v", err)
	}

	// Инициализация Telegram бота
	telegramBot, err := NewTelegramBot(cfg.TelegramToken, cfg.TelegramUserID)
	if err != nil {
		log.Fatalf("Failed to init Telegram bot: %v", err)
	}

	// Получение пароля от почты
	emailPassword, err := telegramBot.RequestPassword(fmt.Sprintf("Password for %s:", cfg.EmailUsername))
	if err != nil {
		log.Fatalf("Failed to get password: %v", err)
	}

	// Инициализация почтового клиента
	emailClient, err := NewEmailClient(cfg.EmailHost, cfg.EmailPort, cfg.EmailUsername, emailPassword)
	if err != nil {
		log.Fatalf("Failed to init email client: %v", err)
	}
	defer emailClient.Close()

	// Обработка graceful shutdown
	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, os.Interrupt, syscall.SIGTERM)

	// Основной цикл обработки
	ticker := time.NewTicker(time.Duration(cfg.CheckIntervalSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			processNewEmails(emailClient, telegramBot)
		case <-shutdownChan:
			log.Println("Shutting down...")
			return
		}
	}
}

func createDefaultConfig() {
	configContent := `[email]
host = imap.example.com
port = 993
username = your@email.com

[telegram]
token = YOUR_TELEGRAM_BOT_TOKEN
user_id = YOUR_TELEGRAM_USER_ID

[app]
check_interval_seconds = 300
`
	if err := os.WriteFile("config.ini", []byte(configContent), 0644); err != nil {
		log.Fatalf("Failed to create config file: %v", err)
	}
	log.Println("Created config.ini template. Please edit it and restart.")
}

func processNewEmails(emailClient *EmailClient, telegramBot *TelegramBot) {
	log.Println("Checking for new emails...")
	uids, err := emailClient.ListNewMailUIDs()
	if err != nil {
		log.Printf("Error listing emails: %v", err)
		return
	}

	for _, uid := range uids {
		_, bytes, err := emailClient.FetchMail(uid)
		if err != nil {
			log.Printf("Error fetching email %d: %v", uid, err)
			continue
		}

		parsedData, err := ParseEmail(bytes)
		if err != nil {
			log.Printf("Error parsing email %d: %v", uid, err)
			continue
		}

		if err := telegramBot.SendEmailData(parsedData); err != nil {
			log.Printf("Error sending email %d to Telegram: %v", uid, err)
			continue
		}

		if err := emailClient.MarkUIDAsProcessed(uid); err != nil {
			log.Printf("Error marking email %d as processed: %v", uid, err)
		}
	}
}
