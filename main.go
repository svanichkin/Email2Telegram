package main

import (
	"fmt"
	"log"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"strings" // Added for strings.TrimSpace
	"sync"
	"syscall"

	"github.com/BrianLeishman/go-imap"
)

func main() {

	log.Println("Starting Email Processor...")

	// Config loading

	cfg, err := LoadConfig(filepath.Base(os.Args[0]))
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Telegram init

	telegramBot, err := NewTelegramBot(cfg.TelegramToken, cfg.TelegramUserId)
	if err != nil {
		log.Fatalf("Failed to init Telegram bot: %v", err)
	}

	// OpenAI Client init
	var openAIClient *OpenAIClient // Declare openAIClient
	openAIClient, err = NewOpenAIClient(cfg.OpenAIToken)
	if err != nil {
		// This error should now only occur if NewOpenAIClient encounters an unexpected issue
		// during client setup (e.g., issues with the openai library itself, not for an empty token).
		log.Printf("Warning: Failed to initialize OpenAI client: %v. OpenAI features will be disabled.", err)
		openAIClient = nil // Ensure it's nil if there was any error
	} else if openAIClient == nil {
		log.Println("OpenAI token not provided or empty. OpenAI features will be disabled.")
	} else {
		log.Println("OpenAI client initialized successfully.")
	}

	// User request for username if needed

	email, password := cfg.GetCred()

	for email == "" {
		email, err = telegramBot.RequestUserInput("Enter your email please...")
		if err != nil {
			log.Printf("Error getting username: %v", err)
			continue
		}
		if _, err := mail.ParseAddress(email); err != nil {
			telegramBot.SendMessage("Email not valid!")
			email = ""
			continue
		}
		cfg.SetCred(email, password)
	}

	// User request for password if needed

	for password == "" {
		password, err = telegramBot.RequestUserInput(fmt.Sprintf("Enter your password for %s, please...", email))
		if err != nil {
			log.Printf("Error getting username: %v", err)
			continue
		}
		imap.RetryCount = 0
		c, err := imap.New(email, password, cfg.EmailImapHost, cfg.EmailImapPort)
		if err != nil {
			log.Printf("Failed to login to server: %v", err)
			telegramBot.SendMessage("Wrong password!")
			password = ""
			continue
		}
		c.Close()
		cfg.SetCred(email, password)
	}

	// Mail init

	var emailClient *EmailClient
	emailClient, err = NewEmailClient(
		cfg.EmailImapHost,
		cfg.EmailImapPort,
		cfg.EmailSmtpHost,
		cfg.EmailSmtpPort,
		email,
		password,
		func() {
			processNewEmails(emailClient, telegramBot, openAIClient)
		})
	if err != nil {
		log.Fatalf("Failed to init email client: %v", err)
	}
	defer emailClient.Close()

	// Telegram listener

	go telegramBot.StartListener(
		func(uid int, message string, files []struct{ Url, Name string }) { // openAIClient removed from params
			replayToEmail(emailClient, telegramBot, uid, message, files) // openAIClient removed from call
		},
		func(to string, title string, message string, files []struct{ Url, Name string }) { // openAIClient removed from params
			sendNewEmail(emailClient, telegramBot, to, title, message, files) // openAIClient removed from call
		},
	)

	processNewEmails(emailClient, telegramBot, openAIClient)

	// Graceful shutdown

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	// Waiting signal OS

	<-signalChan
	log.Println("Shutdown signal received")
}

var mu sync.Mutex

func processNewEmails(emailClient *EmailClient, telegramBot *TelegramBot, openAIClient *OpenAIClient) {

	mu.Lock()
	emailClient.imap.StopIdle()
	defer func() {
		if err := emailClient.startIdleWithHandler(); err != nil {
			telegramBot.SendMessage("Failed to reply email for!")
			return
		}
		mu.Unlock()
	}()

	log.Println("Checking for new emails...")
	uids, err := emailClient.ListNewMailUIDs()
	if err != nil {
		log.Printf("Error listing emails: %v", err)
		return
	}

	// If first star, ignore all letters

	if uids, err = emailClient.AddAllUIDsIfFirstStart(uids); err != nil {
		log.Printf("Error marking UIDs as processed on first start: %v", err)
		return
	}

	// Main cycle for new letters

	for _, uid := range uids {
		mail, err := emailClient.FetchMail(uid)
		if err != nil {
			log.Printf("Error fetching email %d: %v", uid, err)
			continue
		}
		emailData := ParseEmail(mail, uid) // Ensure emailData is populated

		if openAIClient != nil && emailData != nil && emailData.TextBody != "" { // Check if client exists and there's text to process
			log.Printf("Attempting to process email UID %d with OpenAI...", uid)

			// System prompt instructing the AI.
			// The AI is asked to provide a summary and then include the original email.
			systemPrompt := "You are a helpful assistant. Summarize the following email content concisely and professionally. After the summary, clearly label and include the full original email text. Format it like this:\n\nAI Summary:\n[Your concise summary here]\n\n-----\nOriginal Email:\n"

			fullPrompt := systemPrompt + emailData.TextBody

			model := "gpt-4o-mini" // As per issue requirement
			temperature := 0.25   // As per issue requirement

			processedBody, err := openAIClient.GenerateText(fullPrompt, model, float64(temperature))
			if err != nil {
				log.Printf("Error processing email UID %d with OpenAI: %v. Sending original email.", uid, err)
				// No change to emailData.TextBody, original will be sent
			} else if strings.TrimSpace(processedBody) == "" {
				log.Printf("OpenAI returned an empty response for email UID %d. Sending original email.", uid)
				// No change to emailData.TextBody, original will be sent
			} else {
				log.Printf("Successfully processed email UID %d with OpenAI. Updating TextBody.", uid)
				emailData.TextBody = processedBody // Replace original body with AI-processed content
			}
		}

		if err := telegramBot.SendEmailData(emailData); err != nil {
			log.Printf("Error sending email %d to Telegram: %v", uid, err)
			continue
		}
		if err := emailClient.MarkUIDAsProcessed(uid); err != nil {
			log.Printf("Error marking email %d as processed: %v", uid, err)
		}
	}

}

func replayToEmail(emailClient *EmailClient, telegramBot *TelegramBot, uid int, message string, files []struct{ Url, Name string }) {

	mu.Lock()
	emailClient.imap.StopIdle()
	defer func() {
		if err := emailClient.startIdleWithHandler(); err != nil {
			telegramBot.SendMessage("Failed to reply email for!")
			return
		}
		mu.Unlock()
	}()

	err := emailClient.ReplyTo(uid, message, files)
	if err != nil {
		telegramBot.SendMessage("Failed to reply email for!")
	}

}

func sendNewEmail(emailClient *EmailClient, telegramBot *TelegramBot, to, title, message string, files []struct{ Url, Name string }) {

	mu.Lock()
	emailClient.imap.StopIdle()
	defer func() {
		if err := emailClient.startIdleWithHandler(); err != nil {
			telegramBot.SendMessage("Failed to send email!")
			return
		}
		mu.Unlock()
	}()

	err := emailClient.SendMail([]string{to}, title, message, files)
	if err != nil {
		telegramBot.SendMessage("Failed to send email!")
	}

}
