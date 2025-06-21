package main

import (
	"fmt"
	"io"
	"log"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath" // Added for strings.TrimSpace
	"strings"
	"sync"
	"syscall"

	"github.com/BrianLeishman/go-imap"
	"github.com/logrusorgru/aurora/v4"
	// tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5" // Will be removed if all direct uses are gone
)

var au aurora.Aurora

type colorWriter struct {
	io.Writer
}

func (cw *colorWriter) Write(p []byte) (n int, err error) {
	s := string(p)
	timestampAndRest := strings.SplitN(s, " ", 2)
	if len(timestampAndRest) != 2 {
		return cw.Writer.Write(p) // Default if format is unexpected
	}
	timestamp := timestampAndRest[0]
	message := strings.TrimSuffix(timestampAndRest[1], "\n") // Trim newline for coloring

	var coloredMessage aurora.Value
	switch {
	case strings.HasPrefix(message, "Failed to") || strings.HasPrefix(message, "Critical configuration error:") || strings.HasPrefix(message, "Error "):
		coloredMessage = au.Red(message)
	case strings.HasPrefix(message, "Warning:"):
		coloredMessage = au.Yellow(message)
	case strings.HasPrefix(message, "Successfully") || strings.HasPrefix(message, "Sent ") || strings.HasPrefix(message, "Operating in"):
		coloredMessage = au.Green(message)
	case strings.HasPrefix(message, "Note:"):
		coloredMessage = au.Cyan(message)
	case strings.HasPrefix(message, "Starting Email Processor..."): // Specific case for the first message
		coloredMessage = au.Cyan(message)
	default:
		coloredMessage = au.BrightBlack(message) // Default color for other messages
	}
	// Add newline back after coloring
	return cw.Writer.Write([]byte(au.Gray(10, timestamp).String() + " " + coloredMessage.String() + "\n"))
}

func main() {
	au = aurora.New(aurora.WithColors(true))
	log.SetOutput(&colorWriter{Writer: os.Stderr})
	// log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds) // Keep existing flags, colorWriter will handle the coloring part
	// No need to set flags if we want the default log.LstdFlags (date and time)
	// The colorWriter will receive the full log line including the prefix set by log.SetFlags()
	// Let's keep the default flags, which include date and time. Microseconds might be too verbose.
	// The au.Cyan in log.Println below will be handled by the colorWriter now.

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

	if cfg.TelegramChatID != 0 { // This implies recipientID for the bot is cfg.TelegramChatID

		// Check if admin are enabled for the chat
		log.Printf("Checking admin rights for bot in group chat ID: %d", cfg.TelegramChatID)
		adminEnabled, errAdminCheck := telegramBot.CheckAndRequestAdminRights(cfg.TelegramChatID)
		if errAdminCheck != nil {
			log.Printf("Error during CheckAndRequestAdminRights API call: %v", errAdminCheck)
		} else if !adminEnabled {
			// Check was successful, but rights are missing
			messageText := "For correct operation, I need administrator rights in this group chat. Please provide them."
			if sendErr := telegramBot.SendMessage(messageText); sendErr != nil {
				// tb.recipientId is cfg.TelegramChatID in this context
				log.Printf("Failed to send admin rights request message to chat %d: %v", cfg.TelegramChatID, sendErr)
			}
		}

		// Check if topics are enabled for the chat
		topicsEnabled, errTopicsCheck := telegramBot.CheckTopicsEnabled(cfg.TelegramChatID)
		if errTopicsCheck != nil {
			log.Printf("Error checking topics for chat ID %d: %v", cfg.TelegramChatID, errTopicsCheck)
		} else if !topicsEnabled {
			// Check was successful, but topics are not enabled
			notificationText := "Topics are not enabled in this group. Please enable them for proper functionality."
			if sendErr := telegramBot.SendMessage(notificationText); sendErr != nil {
				// tb.recipientId is cfg.TelegramChatID in this context
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
			if errSend := telegramBot.SendMessage("Email not valid!"); errSend != nil {
				log.Printf("Failed to send 'Email not valid' message: %v", errSend)
			}
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
			if errSend := telegramBot.SendMessage("Wrong password!"); errSend != nil {
				log.Printf("Failed to send 'Wrong password' message: %v", errSend)
			}
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
