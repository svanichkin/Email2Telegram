package main

import (
	"bytes"
	"fmt"
	"log"
	"net/mail"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Minimalistic mock for just testing ParseEmail -> ConvertHTMLToPDF path early.
func createSimpleHTMLMockMessage() *mail.Message {
	// HTML content that's more likely to render something visible in a PDF
	htmlBody := `
	<!DOCTYPE html>
	<html>
	<head><title>Test PDF</title></head>
	<body>
		<h1>Hello, Rod!</h1>
		<p>This is a test PDF generation using go-rod.</p>
		<p style="color:blue;">Some blue text.</p>
		<img src="https://www.google.com/images/branding/googlelogo/1x/googlelogo_color_272x92dp.png" alt="Google Logo - Should load if internet is available to rod">
	</body>
	</html>`

	// Encode to base64 to mimic a real email part
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
		// Fallback to an even simpler message if ReadMessage fails
		return &mail.Message{
			Header: map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			Body:   strings.NewReader("<h1>Fallback HTML</h1>"),
		}
	}
	return msg
}

func testRodPDFGeneration() {
	log.Println("--- Running initial Rod PDF Generation Test ---")
	mockMsg := createSimpleHTMLMockMessage()
	parsedData, err := ParseEmail(mockMsg)
	if err != nil {
		log.Printf("Rod PDF Test: Error parsing mock email: %v", err)
	} else {
		if parsedData.PDFBody != nil && len(parsedData.PDFBody) > 0 {
			log.Printf("Rod PDF Test: Successfully generated PDF. Size: %d bytes.", len(parsedData.PDFBody))
			// Optionally, save for inspection:
			// _ = os.WriteFile("rod_test.pdf", parsedData.PDFBody, 0644)
			// log.Println("Rod PDF Test: Saved rod_test.pdf for inspection.")
		} else {
			log.Println("Rod PDF Test: PDFBody is nil or empty after parsing. Check previous logs from ParseEmail/ConvertHTMLToPDF for errors (e.g., browser launch issues).")
		}
	}
	log.Println("--- Rod PDF Generation Test Finished ---")
}

func main() {
	log.Println("Starting Email Bot...")

	// --- Run a direct test for Rod PDF generation early ---
	// This ensures the PDF path is tested regardless of IMAP availability or email content.
	testRodPDFGeneration()

	// --- Load Configuration ---
	cfg, err := LoadConfig("config.ini")
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("'config.ini' not found. Creating a dummy 'config.ini'. Please fill it out and restart the bot.")
			dummyConfigContent := `
[email]
host = imap.example.com
port = 993
username = user@example.com

[telegram]
token = YOUR_TELEGRAM_BOT_TOKEN
user_id = YOUR_TELEGRAM_USER_ID_AS_INTEGER

[app]
check_interval_seconds = 300
`
			if writeErr := os.WriteFile("config.ini", []byte(dummyConfigContent), 0644); writeErr != nil {
				log.Fatalf("Failed to write dummy config.ini: %v", writeErr)
			}
			return
		}
		log.Fatalf("Failed to load configuration: %v", err)
	}
	log.Printf("Configuration loaded: EmailHost=%s, EmailPort=%d, EmailUsername=%s, CheckIntervalSeconds=%d",
		cfg.EmailHost, cfg.EmailPort, cfg.EmailUsername, cfg.CheckIntervalSeconds)

	if cfg.TelegramToken == "" || cfg.TelegramToken == "YOUR_TELEGRAM_BOT_TOKEN" {
		log.Fatalf("Telegram token is not configured in config.ini. Please set it and restart.")
	}
	if cfg.TelegramUserID == 0 {
		log.Fatalf("Telegram user ID is not configured in config.ini. Please set it and restart.")
	}
	telegramBot, err := NewTelegramBot(cfg.TelegramToken, cfg.TelegramUserID)
	if err != nil {
		log.Fatalf("Failed to initialize Telegram bot: %v", err)
	}
	log.Println("Telegram bot initialized successfully.")

	var emailPassword string
	log.Println("Requesting email password via Telegram...")
	passwordPrompt := fmt.Sprintf("Please reply with the IMAP password for %s:", cfg.EmailUsername)
	emailPassword, err = telegramBot.RequestPassword(passwordPrompt)
	if err != nil {
		log.Fatalf("Failed to get email password from Telegram: %v", err)
	}
	if emailPassword == "" {
		log.Fatalf("Received an empty password from Telegram. Exiting.")
	}
	log.Println("Email password received.")

	log.Println("Initializing email client...")
	emailClient, err := NewEmailClient(cfg.EmailHost, cfg.EmailPort, cfg.EmailUsername, emailPassword)
	if err != nil {
		log.Fatalf("Failed to initialize email client: %v", err)
	}
	defer func() {
		log.Println("Closing email client connection...")
		emailClient.Close()
		log.Println("Email client closed.")
	}()
	log.Println("Email client initialized successfully.")

	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, os.Interrupt, syscall.SIGTERM)
	log.Println("Graceful shutdown handler installed. Press Ctrl+C to exit.")

	log.Println("Starting main processing loop...")
	ticker := time.NewTicker(time.Duration(cfg.CheckIntervalSeconds) * time.Second)
	defer ticker.Stop()

Loop:
	for {
		select {
		case <-ticker.C:
			log.Println("Checking for new emails...")
			newUIDs, err := emailClient.ListNewMailUIDs()
			if err != nil {
				log.Printf("Error listing new mail UIDs: %v. Skipping this check.", err)
				continue Loop
			}

			if len(newUIDs) > 0 {
				log.Printf("Found %d new email(s). Processing...", len(newUIDs))
				for _, uid := range newUIDs {
					log.Printf("Processing email with UID %d...", uid)

					fetchedMail, fetchErr := emailClient.FetchMail(uid)
					if fetchErr != nil {
						log.Printf("Error fetching mail with UID %d: %v. Skipping this email.", uid, fetchErr)
						continue
					}
					log.Printf("Successfully fetched email UID %d: Subject '%s'", uid, fetchedMail.Header.Get("Subject"))

					parsedData, parseErr := ParseEmail(fetchedMail)
					if parseErr != nil {
						log.Printf("Error parsing email with UID %d: %v. Skipping this email.", uid, parseErr)
						continue
					}
					log.Printf("Successfully parsed email UID %d.", uid)
					// Logging of PDF success/failure is now primarily within ParseEmail/ConvertHTMLToPDF
					// and the SendEmailData function if a PDF is attached.

					sendErr := telegramBot.SendEmailData(parsedData)
					if sendErr != nil {
						log.Printf("Error sending email data (UID %d) to Telegram: %v.", uid, sendErr)
					} else {
						log.Printf("Successfully sent email data (UID %d) to Telegram.", uid)
					}

					if fetchErr == nil && parseErr == nil {
						markErr := emailClient.MarkUIDAsProcessed(uid)
						if markErr != nil {
							log.Printf("Error marking email UID %d as processed: %v.", uid, markErr)
						} else {
							log.Printf("Successfully marked email UID %d as processed.", uid)
						}
					} else {
						log.Printf("Skipping marking UID %d as processed due to earlier errors in fetching or parsing.", uid)
					}
				}
			} else {
				log.Println("No new emails found.")
			}

		case sig := <-shutdownChan:
			log.Printf("Received signal: %s. Starting graceful shutdown...", sig)
			break Loop
		}
	}

	log.Println("Email Bot has shut down.")
}
