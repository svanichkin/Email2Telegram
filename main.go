package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.Println("Starting Email Bot...")

	// --- Load Configuration ---
	cfg, err := LoadConfig("config.ini")
	if err != nil {
		// If config.ini doesn't exist, create a dummy one and instruct user
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
			// No point in continuing if config needs to be filled.
			return
		}
		log.Fatalf("Failed to load configuration: %v", err)
	}
	log.Printf("Configuration loaded: EmailHost=%s, EmailPort=%d, EmailUsername=%s, CheckIntervalSeconds=%d",
		cfg.EmailHost, cfg.EmailPort, cfg.EmailUsername, cfg.CheckIntervalSeconds)

	// --- Initialize Telegram Bot ---
	if cfg.TelegramToken == "" || cfg.TelegramToken == "YOUR_TELEGRAM_BOT_TOKEN" {
		log.Fatalf("Telegram token is not configured in config.ini. Please set it and restart.")
		return
	}
	if cfg.TelegramUserID == 0 {
		log.Fatalf("Telegram user ID is not configured in config.ini. Please set it and restart.")
		return
	}
	telegramBot, err := NewTelegramBot(cfg.TelegramToken, cfg.TelegramUserID)
	if err != nil {
		log.Fatalf("Failed to initialize Telegram bot: %v", err)
	}
	log.Println("Telegram bot initialized successfully.")

	// --- Get Email Password ---
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

	// --- Initialize Email Client ---
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

	// --- Graceful Shutdown Setup ---
	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, os.Interrupt, syscall.SIGTERM)
	log.Println("Graceful shutdown handler installed. Press Ctrl+C to exit.")

	// --- Main Processing Loop ---
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
				// Potentially implement more robust error handling here, e.g., retry logic or temporary backoff
				continue Loop
			}

			if len(newUIDs) > 0 {
				log.Printf("Found %d new email(s). Processing...", len(newUIDs))
				for _, uid := range newUIDs {
					log.Printf("Processing email with UID %d...", uid)

					// 1. Fetch Mail
					fetchedMail, fetchErr := emailClient.FetchMail(uid)
					if fetchErr != nil {
						log.Printf("Error fetching mail with UID %d: %v. Skipping this email.", uid, fetchErr)
						continue // Process next UID
					}
					log.Printf("Successfully fetched email UID %d: Subject '%s'", uid, fetchedMail.Header.Get("Subject"))

					// 2. Parse Email
					parsedData, parseErr := ParseEmail(fetchedMail)
					if parseErr != nil {
						log.Printf("Error parsing email with UID %d: %v. Skipping this email.", uid, parseErr)
						// Consider if we should mark as processed or retry later if parsing fails
						continue // Process next UID
					}
					log.Printf("Successfully parsed email UID %d.", uid)

					// 3. Send to Telegram
					sendErr := telegramBot.SendEmailData(parsedData)
					if sendErr != nil {
						log.Printf("Error sending email data (UID %d) to Telegram: %v.", uid, sendErr)
						// Decide on retry or if marking as processed is appropriate
						// For now, we'll continue and attempt to mark as processed to avoid reprocessing loops on send errors
					} else {
						log.Printf("Successfully sent email data (UID %d) to Telegram.", uid)
					}

					// 4. Mark as Processed
					// This is critical. If sending to Telegram fails, we might not want to mark as processed
					// to allow for retries. However, for this example, we mark as processed if fetch and parse were okay.
					// A more robust system might have a separate mechanism for failed Telegram sends.
					if fetchErr == nil && parseErr == nil { // Only mark if critical steps succeeded
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
			// Perform any pre-shutdown cleanup if necessary before defers run
			break Loop // Exit the main loop
		}
	}

	log.Println("Email Bot has shut down.")
}
