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

func main() {
	au = aurora.New(aurora.WithColors(true))
	// Revert log output to default stderr and default flags
	log.SetOutput(os.Stderr)
	log.SetFlags(0) // Remove default flags, we'll format it manually


	log.Println(au.Gray(11, "[INIT]"), au.Cyan("Email Processor"), au.Green(aurora.Bold("started")))

	// Config loading
	log.Println(au.Gray(11, "[CONFIG]"), au.Yellow("Loading config..."))
	cfg, err := LoadConfig(filepath.Base(os.Args[0]))
	if err != nil {
		log.Fatalln(au.Gray(11, "[CONFIG]"), au.Red(aurora.Bold("Failed to load config:")), au.Red(err))
	}
	log.Println(au.Gray(11, "[CONFIG]"), au.Green("Config loaded successfully"))

	// Determine recipientID for Telegram bot
	log.Println(au.Gray(11, "[TELEGRAM]"), au.Yellow("Determining recipient ID..."))
	var recipientID int64
	if cfg.TelegramChatID != 0 {
		recipientID = cfg.TelegramChatID
		log.Println(au.Gray(11, "[TELEGRAM]"), au.Green(fmt.Sprintf("Operating in group chat mode. Chat ID: %d", recipientID)))
		if cfg.TelegramUserId != 0 {
			log.Println(au.Gray(11, "[TELEGRAM]"), au.Cyan(fmt.Sprintf("Note: Telegram UserID (%d) is also set, but ChatID (%d) takes precedence.", cfg.TelegramUserId, cfg.TelegramChatID)))
		}
	} else if cfg.TelegramUserId != 0 {
		recipientID = cfg.TelegramUserId
		log.Println(au.Gray(11, "[TELEGRAM]"), au.Green(fmt.Sprintf("Operating in direct user message mode. User ID: %d", recipientID)))
	} else {
		log.Fatalln(au.Gray(11, "[CONFIG]"), au.Red(aurora.Bold("Critical configuration error:")), au.Red(fmt.Sprintf("Neither Telegram UserID nor ChatID is set. Token presence: %t", cfg.TelegramToken != "")))
	}

	// Telegram init
	log.Println(au.Gray(11, "[TELEGRAM]"), au.Yellow("Initializing Telegram bot..."))
	telegramBot, err := NewTelegramBot(cfg.TelegramToken, recipientID)
	if err != nil {
		log.Fatalln(au.Gray(11, "[TELEGRAM]"), au.Red(aurora.Bold("Failed to init Telegram bot:")), au.Red(err))
	}
	log.Println(au.Gray(11, "[TELEGRAM]"), au.Green("Telegram bot initialized successfully"))

	if cfg.TelegramChatID != 0 { // This implies recipientID for the bot is cfg.TelegramChatID
		log.Println(au.Gray(11, "[TELEGRAM]"), au.Yellow(fmt.Sprintf("Checking admin rights for bot in group chat ID: %d", cfg.TelegramChatID)))
		adminEnabled, errAdminCheck := telegramBot.CheckAndRequestAdminRights(cfg.TelegramChatID)
		if errAdminCheck != nil {
			log.Println(au.Gray(11, "[TELEGRAM]"), au.Red("Error during CheckAndRequestAdminRights API call:"), au.Red(errAdminCheck))
		} else if !adminEnabled {
			log.Println(au.Gray(11, "[TELEGRAM]"), au.Yellow("Bot lacks admin rights. Requesting..."))
			messageText := "For correct operation, I need administrator rights in this group chat. Please provide them."
			if sendErr := telegramBot.SendMessage(messageText); sendErr != nil {
				log.Println(au.Gray(11, "[TELEGRAM]"), au.Red(fmt.Sprintf("Failed to send admin rights request to chat %d:", cfg.TelegramChatID)), au.Red(sendErr))
			}
		} else {
			log.Println(au.Gray(11, "[TELEGRAM]"), au.Green("Bot has admin rights."))
		}

		log.Println(au.Gray(11, "[TELEGRAM]"), au.Yellow(fmt.Sprintf("Checking topics enabled for chat ID: %d", cfg.TelegramChatID)))
		topicsEnabled, errTopicsCheck := telegramBot.CheckTopicsEnabled(cfg.TelegramChatID)
		if errTopicsCheck != nil {
			log.Println(au.Gray(11, "[TELEGRAM]"), au.Red("Error checking topics:"), au.Red(errTopicsCheck))
		} else if !topicsEnabled {
			log.Println(au.Gray(11, "[TELEGRAM]"), au.Yellow("Topics not enabled. Notifying chat..."))
			notificationText := "Topics are not enabled in this group. Please enable them for proper functionality."
			if sendErr := telegramBot.SendMessage(notificationText); sendErr != nil {
				log.Println(au.Gray(11, "[TELEGRAM]"), au.Red(fmt.Sprintf("Error sending 'topics not enabled' notification to chat ID %d:", cfg.TelegramChatID)), au.Red(sendErr))
			} else {
				log.Println(au.Gray(11, "[TELEGRAM]"), au.Green(fmt.Sprintf("Sent 'topics not enabled' notification to chat ID %d.", cfg.TelegramChatID)))
			}
		} else {
			log.Println(au.Gray(11, "[TELEGRAM]"), au.Green("Topics are enabled."))
		}
	}

	// OpenAI Client init
	log.Println(au.Gray(11, "[OPENAI]"), au.Yellow("Initializing OpenAI client..."))
	var openAIClient *OpenAIClient
	openAIClient, err = NewOpenAIClient(cfg.OpenAIToken)
	if err != nil {
		log.Println(au.Gray(11, "[OPENAI]"), au.Yellow(fmt.Sprintf("Warning: Failed to initialize OpenAI client: %v. Features disabled.", err)))
		openAIClient = nil
	} else if openAIClient == nil {
		log.Println(au.Gray(11, "[OPENAI]"), au.Yellow("OpenAI token not provided. Features disabled."))
	} else {
		log.Println(au.Gray(11, "[OPENAI]"), au.Green("OpenAI client initialized successfully."))
	}

	// User request for username if needed
	email, password := cfg.GetCred()
	log.Println(au.Gray(11, "[AUTH]"), au.Yellow("Checking credentials..."))

	for email == "" {
		log.Println(au.Gray(11, "[AUTH]"), au.Yellow("Email not found in config. Requesting input..."))
		email, err = telegramBot.RequestUserInput("Enter your email please...")
		if err != nil {
			log.Println(au.Gray(11, "[AUTH]"), au.Red("Error getting username from input:"), au.Red(err))
			continue
		}
		if _, err := mail.ParseAddress(email); err != nil {
			log.Println(au.Gray(11, "[AUTH]"), au.Yellow("Invalid email format entered by user."))
			if errSend := telegramBot.SendMessage("Email not valid!"); errSend != nil {
				log.Println(au.Gray(11, "[TELEGRAM]"), au.Red("Failed to send 'Email not valid' message:"), au.Red(errSend))
			}
			email = "" // Reset to re-trigger input
			continue
		}
		cfg.SetCred(email, password)
		log.Println(au.Gray(11, "[AUTH]"), au.Green("Email obtained and saved to config."))
	}

	for password == "" {
		log.Println(au.Gray(11, "[AUTH]"), au.Yellow(fmt.Sprintf("Password not found for %s. Requesting input...", email)))
		password, err = telegramBot.RequestUserInput(fmt.Sprintf("Enter your password for %s, please...", email))
		if err != nil {
			log.Println(au.Gray(11, "[AUTH]"), au.Red("Error getting password from input:"), au.Red(err))
			continue
		}
		log.Println(au.Gray(11, "[AUTH]"), au.Yellow("Attempting IMAP login to validate password..."))
		imap.RetryCount = 0 // Reset for this specific check
		c, err := imap.New(email, password, cfg.EmailImapHost, cfg.EmailImapPort)
		if err != nil {
			log.Println(au.Gray(11, "[AUTH]"), au.Red("Failed to login to IMAP server (likely wrong password):"), au.Red(err))
			if errSend := telegramBot.SendMessage("Wrong password!"); errSend != nil {
				log.Println(au.Gray(11, "[TELEGRAM]"), au.Red("Failed to send 'Wrong password' message:"), au.Red(errSend))
			}
			password = "" // Reset to re-trigger input
			continue
		}
		c.Close() // Close test connection
		cfg.SetCred(email, password)
		log.Println(au.Gray(11, "[AUTH]"), au.Green("Password validated and saved to config."))
	}
	log.Println(au.Gray(11, "[AUTH]"), au.Green("Credentials ready."))

	// Mail init
	log.Println(au.Gray(11, "[EMAIL]"), au.Yellow("Initializing Email client..."))
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
		log.Fatalln(au.Gray(11, "[EMAIL]"), au.Red(aurora.Bold("Failed to init email client:")), au.Red(err))
	}
	defer emailClient.Close()
	log.Println(au.Gray(11, "[EMAIL]"), au.Green("Email client initialized successfully."))

	// Telegram listener
	log.Println(au.Gray(11, "[TELEGRAM]"), au.Yellow("Starting Telegram listener..."))
	go telegramBot.StartListener(
		func(uid int, message string, files []struct{ Url, Name string }) {
			replayToEmail(emailClient, telegramBot, uid, message, files)
		},
		func(to string, title string, message string, files []struct{ Url, Name string }) {
			sendNewEmail(emailClient, telegramBot, to, title, message, files)
		},
	)
	log.Println(au.Gray(11, "[TELEGRAM]"), au.Green("Telegram listener started."))

	log.Println(au.Gray(11, "[EMAIL]"), au.Yellow("Performing initial email check..."))
	processNewEmails(emailClient, telegramBot, openAIClient)

	// Graceful shutdown
	log.Println(au.Gray(11, "[SYSTEM]"), au.Yellow("Setting up graceful shutdown listener..."))
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	log.Println(au.Gray(11, "[SYSTEM]"), au.Green("Application started. Waiting for signals or new emails..."))
	<-signalChan
	log.Println(au.Gray(11, "[SYSTEM]"), au.Magenta(aurora.Bold("Shutdown signal received.")))
}

var mu sync.Mutex

func processNewEmails(emailClient *EmailClient, telegramBot *TelegramBot, openAIClient *OpenAIClient) {
	log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Cyan("Acquiring lock for email processing..."))
	mu.Lock()
	log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Cyan("Stopping IMAP IDLE..."))
	emailClient.imap.StopIdle() // TODO: Error handling for StopIdle?
	defer func() {
		log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Cyan("Attempting to restart IMAP IDLE..."))
		if err := emailClient.startIdleWithHandler(); err != nil {
			// This log might be problematic if telegramBot itself has issues.
			log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Red("Failed to restart IMAP IDLE:"), au.Red(err))
			telegramBot.SendMessage("Critical error: Failed to restart email listening!") // Consider a more robust notification
			return
		}
		log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Green("IMAP IDLE restarted."))
		mu.Unlock()
		log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Cyan("Email processing lock released."))
	}()

	log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Yellow("Checking for new emails..."))
	uids, err := emailClient.ListNewMailUIDs()
	if err != nil {
		log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Red("Error listing new email UIDs:"), au.Red(err))
		return
	}
	log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Green(fmt.Sprintf("Found %d potentially new email(s).", len(uids))))

	// If first start, ignore all letters
	log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Yellow("Adjusting UIDs for first start if necessary..."))
	adjustedUIDs, err := emailClient.AddAllUIDsIfFirstStart(uids)
	if err != nil {
		log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Red("Error adjusting UIDs on first start:"), au.Red(err))
		return // Or should we proceed with original uids? This implies a state-saving error.
	}
	if len(uids) > 0 && len(adjustedUIDs) == 0 {
		log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Cyan("First start detected, all existing emails marked as processed."))
	}
	uids = adjustedUIDs // Use the (potentially) modified list

	// Main cycle for new letters
	if len(uids) > 0 {
		log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Yellow(fmt.Sprintf("Processing %d new email(s)...", len(uids))))
	}
	for _, uid := range uids {
		log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Cyan(fmt.Sprintf("Fetching email with UID: %d", uid)))
		mail, err := emailClient.FetchMail(uid)
		if err != nil {
			log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Red(fmt.Sprintf("Error fetching email UID %d:", uid)), au.Red(err))
			continue
		}
		log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Green(fmt.Sprintf("Successfully fetched email UID %d. Parsing...", uid)))
		emailData := ParseEmail(mail, uid) // Assuming ParseEmail is robust

		isCode := false // Placeholder, actual logic might be complex
		isSpam := false // Placeholder

		if openAIClient != nil && emailData != nil && emailData.TextBody != "" {
			log.Println(au.Gray(11, "[OPENAI]"), au.Yellow(fmt.Sprintf("Attempting to process email UID %d with OpenAI...", uid)))
			result, err := openAIClient.GenerateTextFromEmail(emailData.Subject + " " + emailData.From + " " + emailData.TextBody)
			if err != nil {
				log.Println(au.Gray(11, "[OPENAI]"), au.Red(fmt.Sprintf("Failed to process email UID %d with OpenAI:", uid)), au.Red(err), au.Yellow("Sending original email."))
				// isSpam remains false, isCode remains false
			} else {
				log.Println(au.Gray(11, "[OPENAI]"), au.Green(fmt.Sprintf("OpenAI processing for UID %d successful. Spam: %t, Code: '%s'", uid, result.IsSpam, result.Code)))
				isSpam = result.IsSpam
				if result.Code != "" {
					isCode = true
					// Potentially override emailData.TextBody with result.Code or handle differently
					log.Println(au.Gray(11, "[OPENAI]"), au.Cyan(fmt.Sprintf("Code found by OpenAI for UID %d: %s", uid, result.Code)))
				}
			}
		} else if emailData == nil || emailData.TextBody == "" {
			log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Yellow(fmt.Sprintf("Skipping OpenAI for UID %d due to nil EmailData or empty TextBody.", uid)))
		}


		log.Println(au.Gray(11, "[TELEGRAM]"), au.Yellow(fmt.Sprintf("Sending email UID %d to Telegram...", uid)))
		if err := telegramBot.SendEmailData(emailData, isCode, isSpam); err != nil {
			log.Println(au.Gray(11, "[TELEGRAM]"), au.Red(fmt.Sprintf("Error sending email UID %d to Telegram:", uid)), au.Red(err))
			continue // Or should we still mark as processed? Depends on desired retry behavior.
		}
		log.Println(au.Gray(11, "[TELEGRAM]"), au.Green(fmt.Sprintf("Successfully sent email UID %d to Telegram.", uid)))

		log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Yellow(fmt.Sprintf("Marking email UID %d as processed...", uid)))
		if err := emailClient.MarkUIDAsProcessed(uid); err != nil {
			log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Red(fmt.Sprintf("Error marking email UID %d as processed:", uid)), au.Red(err))
		} else {
			log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Green(fmt.Sprintf("Successfully marked email UID %d as processed.", uid)))
		}
	}
	if len(uids) == 0 {
		log.Println(au.Gray(11, "[EMAIL_PROC]"), au.Green("No new emails to process."))
	}
}

func replayToEmail(emailClient *EmailClient, telegramBot *TelegramBot, uid int, message string, files []struct{ Url, Name string }) {
	log.Println(au.Gray(11, "[REPLY_EMAIL]"), au.Cyan(fmt.Sprintf("Preparing to reply to email UID: %d", uid)))
	mu.Lock()
	log.Println(au.Gray(11, "[REPLY_EMAIL]"), au.Cyan("Stopping IMAP IDLE..."))
	emailClient.imap.StopIdle()
	defer func() {
		log.Println(au.Gray(11, "[REPLY_EMAIL]"), au.Cyan("Attempting to restart IMAP IDLE..."))
		if err := emailClient.startIdleWithHandler(); err != nil {
			log.Println(au.Gray(11, "[REPLY_EMAIL]"), au.Red("Failed to restart IMAP IDLE after reply:"), au.Red(err))
			telegramBot.SendMessage(fmt.Sprintf("Critical error replying to UID %d: Failed to restart email listening!", uid))
			return
		}
		log.Println(au.Gray(11, "[REPLY_EMAIL]"), au.Green("IMAP IDLE restarted after reply."))
		mu.Unlock()
		log.Println(au.Gray(11, "[REPLY_EMAIL]"), au.Cyan("Reply processing lock released."))
	}()

	log.Println(au.Gray(11, "[REPLY_EMAIL]"), au.Yellow(fmt.Sprintf("Sending reply for UID %d...", uid)))
	err := emailClient.ReplyTo(uid, message, files)
	if err != nil {
		log.Println(au.Gray(11, "[REPLY_EMAIL]"), au.Red(fmt.Sprintf("Failed to send reply for UID %d:", uid)), au.Red(err))
		telegramBot.SendMessage(fmt.Sprintf("Failed to send reply for email (original UID %d)!", uid)) // Inform user
	} else {
		log.Println(au.Gray(11, "[REPLY_EMAIL]"), au.Green(fmt.Sprintf("Successfully sent reply for UID %d.", uid)))
	}
}

func sendNewEmail(emailClient *EmailClient, telegramBot *TelegramBot, to, title, message string, files []struct{ Url, Name string }) {
	log.Println(au.Gray(11, "[SEND_EMAIL]"), au.Cyan(fmt.Sprintf("Preparing to send new email to: %s", to)))
	mu.Lock()
	log.Println(au.Gray(11, "[SEND_EMAIL]"), au.Cyan("Stopping IMAP IDLE..."))
	emailClient.imap.StopIdle()
	defer func() {
		log.Println(au.Gray(11, "[SEND_EMAIL]"), au.Cyan("Attempting to restart IMAP IDLE..."))
		if err := emailClient.startIdleWithHandler(); err != nil {
			log.Println(au.Gray(11, "[SEND_EMAIL]"), au.Red("Failed to restart IMAP IDLE after sending new email:"), au.Red(err))
			telegramBot.SendMessage(fmt.Sprintf("Critical error sending email to %s: Failed to restart email listening!", to))
			return
		}
		log.Println(au.Gray(11, "[SEND_EMAIL]"), au.Green("IMAP IDLE restarted after sending new email."))
		mu.Unlock()
		log.Println(au.Gray(11, "[SEND_EMAIL]"), au.Cyan("New email processing lock released."))
	}()

	log.Println(au.Gray(11, "[SEND_EMAIL]"), au.Yellow(fmt.Sprintf("Sending new email to %s, Title: %s...", to, title)))
	err := emailClient.SendMail([]string{to}, title, message, files)
	if err != nil {
		log.Println(au.Gray(11, "[SEND_EMAIL]"), au.Red(fmt.Sprintf("Failed to send new email to %s:", to)), au.Red(err))
		telegramBot.SendMessage(fmt.Sprintf("Failed to send new email to %s!", to)) // Inform user
	} else {
		log.Println(au.Gray(11, "[SEND_EMAIL]"), au.Green(fmt.Sprintf("Successfully sent new email to %s.", to)))
	}
}
