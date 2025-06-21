package main

import (
	"fmt"
	"log"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath" // Added for strings.TrimSpace
	"sync"
	"syscall"

	"github.com/BrianLeishman/go-imap"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func main() {

	log.Println("Starting Email Processor...")

	// Config loading

	cfg, err := LoadConfig(filepath.Base(os.Args[0]))
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Determine recipientID for Telegram bot

	var recipientID int64
	if cfg.TelegramChatID != 0 {
		recipientID = cfg.TelegramChatID
		log.Printf("Operating in group chat mode. Chat ID: %d", recipientID)
		if cfg.TelegramUserId != 0 {
			log.Printf("Note: Telegram UserID (%d) is also set in config, but ChatID (%d) takes precedence for bot operations.", cfg.TelegramUserId, cfg.TelegramChatID)
		}
	} else if cfg.TelegramUserId != 0 {
		recipientID = cfg.TelegramUserId
		log.Printf("Operating in direct user message mode. User ID: %d", recipientID)
	} else {
		log.Fatalf("Critical configuration error: Neither Telegram UserID nor ChatID is set after config load. Token presence: %t", cfg.TelegramToken != "")
	}

	// Telegram init

	telegramBot, err := NewTelegramBot(cfg.TelegramToken, recipientID)
	if err != nil {
		log.Fatalf("Failed to init Telegram bot: %v", err)
	}

	if cfg.TelegramChatID != 0 {

		// Check if admin are enabled for the chat

		log.Printf("Checking admin rights for bot in group chat ID: %d", cfg.TelegramChatID)
		adminEnabled, err := telegramBot.CheckAndRequestAdminRights(cfg.TelegramChatID)
		if err != nil {
			log.Printf("Error during CheckAndRequestAdminRights: %v", err)
		} else if !adminEnabled {
			messageText := "For correct operation, I need administrator rights in this group chat. Please provide them."
			msg := tgbotapi.NewMessage(cfg.TelegramChatID, messageText)
			if _, sendErr := telegramBot.api.Send(msg); sendErr != nil {
				log.Printf("failed to send admin rights request message to chat %d: %w", cfg.TelegramChatID, sendErr)
			}
		}

		// Check if topics are enabled for the chat

		topicsEnabled, err := telegramBot.CheckTopicsEnabled(cfg.TelegramChatID)
		if err != nil {
			log.Printf("Error checking topics for chat ID %d: %v", cfg.TelegramChatID, err)
		} else if !topicsEnabled {
			notificationText := "Topics are not enabled in this group. Please enable them for proper functionality."
			notificationMsg := tgbotapi.NewMessage(cfg.TelegramChatID, notificationText)
			if _, sendErr := telegramBot.api.Send(notificationMsg); sendErr != nil {
				log.Printf("Error sending 'topics not enabled' notification to chat ID %d: %v", cfg.TelegramChatID, sendErr)
			} else {
				log.Printf("Sent 'topics not enabled' notification to chat ID %d.", cfg.TelegramChatID)
			}
		}
	}

	// OpenAI Client init

	var openAIClient *OpenAIClient
	openAIClient, err = NewOpenAIClient(cfg.OpenAIToken)
	if err != nil {
		log.Printf("Warning: Failed to initialize OpenAI client: %v. OpenAI features will be disabled.", err)
		openAIClient = nil
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
		func(uid int, message string, files []struct{ Url, Name string }) {
			replayToEmail(emailClient, telegramBot, uid, message, files)
		},
		func(to string, title string, message string, files []struct{ Url, Name string }) {
			sendNewEmail(emailClient, telegramBot, to, title, message, files)
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
		emailData := ParseEmail(mail, uid)

		isCode := false
		isSpam := false
		if openAIClient != nil && emailData != nil && emailData.TextBody != "" {
			log.Printf("Attempting to process email UID %d with OpenAI...", uid)
			result, err := openAIClient.GenerateTextFromEmail(emailData.Subject + " " + emailData.From + " " + emailData.TextBody)
			if err != nil {
				log.Printf("Failed to process email UID %d with OpenAI: %v. Sending original email.", uid, err)
			}
			isSpam = result.IsSpam
		}

		if err := telegramBot.SendEmailData(emailData, isCode, isSpam); err != nil {
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
