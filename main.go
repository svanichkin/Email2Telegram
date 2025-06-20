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
		// Call a new method on telegramBot that we will define in telegram_bot.go
		// This method will check for admin rights and send a message if needed.
		// We'll pass the chat ID to it.
		log.Printf("Checking admin rights for bot in group chat ID: %d", cfg.TelegramChatID)
		err := telegramBot.CheckAndRequestAdminRights(cfg.TelegramChatID)
		if err != nil {
			log.Printf("Error during CheckAndRequestAdminRights: %v", err)
			// Depending on the severity or desired behavior, you might choose to
			// send a message to the user/group or handle it differently.
			// For now, just logging.
		}

		log.Printf("Checking if topics are enabled for chat ID %d during startup...", cfg.TelegramChatID)
		topicsEnabled, err := telegramBot.CheckTopicsEnabled(cfg.TelegramChatID)
		if err != nil {
			log.Printf("Error checking topics for chat ID %d during startup: %v", cfg.TelegramChatID, err)
		} else if !topicsEnabled {
			log.Printf("Topics are not enabled for chat ID %d. Sending notification.", cfg.TelegramChatID)
			notificationText := "В этой группе не включены темы. Пожалуйста, включите их для корректной работы."
			notificationMsg := tgbotapi.NewMessage(cfg.TelegramChatID, notificationText)
			if _, sendErr := telegramBot.api.Send(notificationMsg); sendErr != nil { // Note: telegramBot.api is used here
				log.Printf("Failed to send 'topics not enabled' notification to chat ID %d during startup: %v", cfg.TelegramChatID, sendErr)
			} else {
				log.Printf("Successfully sent 'topics not enabled' notification to chat ID %d during startup.", cfg.TelegramChatID)
			}
		} else {
			log.Printf("Topics are enabled for chat ID %d. '📥' topic creation was attempted by CheckTopicsEnabled.", cfg.TelegramChatID)
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
		if openAIClient != nil && emailData != nil && emailData.TextBody != "" {
			log.Printf("Attempting to process email UID %d with OpenAI...", uid)
			result, err := openAIClient.GenerateTextFromEmail(emailData.Subject + " " + emailData.From + " " + emailData.TextBody)
			if err != nil {
				log.Printf("Failed to process email UID %d with OpenAI: %v. Sending original email.", uid, err)
			} else if result.IsSpam {
				emailData.Subject = "🚫 " + emailData.Subject
				emailData.TextBody = result.Summary
			} else if result.Code != "" {
				isCode = true
				emailData.Subject = "🔑 " + emailData.Subject
				emailData.TextBody = result.Code
			} else {
				emailData.Subject = "✉️ " + emailData.Subject
			}
		}

		if err := telegramBot.SendEmailData(emailData, isCode); err != nil {
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
