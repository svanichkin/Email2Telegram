package main

import (
	"fmt"
	"log"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"strings"

	"github.com/BrianLeishman/go-imap"
	"github.com/logrusorgru/aurora/v4"
)

var au aurora.Aurora

func main() {

	// Colorful logs

	au = *aurora.New(aurora.WithColors(true))
	log.SetOutput(os.Stderr)
	log.SetFlags(0)

	log.Println(au.Gray(12, "[INIT]"), au.Cyan("Email Processor"), au.Green(aurora.Bold("starting...")))

	// Config loading

	cfg, err := LoadConfig(filepath.Base(os.Args[0]))
	if err != nil {
		log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red(aurora.Bold("Failed to load config: %v")).String(), err)
	}

	// Chat id or user id

	var rid int64
	if cfg.TelegramChatId != 0 {
		rid = cfg.TelegramChatId
		log.Printf(au.Gray(12, "[INIT]").String()+" "+au.Blue("Operating in group chat mode. Chat ID: %d").String(), rid)
		if cfg.TelegramUserId != 0 {
			log.Printf(au.Gray(12, "[INIT]").String()+" "+au.Yellow("Note: Telegram UserID (%d) is also set in config, but ChatID (%d) takes precedence for bot operations.").String(), cfg.TelegramUserId, cfg.TelegramChatId)
		}
	} else if cfg.TelegramUserId != 0 {
		rid = cfg.TelegramUserId
		log.Printf(au.Gray(12, "[INIT]").String()+" "+au.Blue("Operating in direct user message mode. User ID: %d").String(), rid)
	} else {
		log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red(aurora.Bold("Critical configuration error: Neither Telegram UserID nor ChatID is set after config load. Token presence: %t")).String(), cfg.TelegramToken != "")
	}

	// Telegram init

	tb, err := NewTelegramBot(cfg.TelegramToken, rid)
	if err != nil {
		log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red(aurora.Bold("Failed to init Telegram bot: %v")).String(), err)
	}

	// Check permissions if group mode

	if cfg.TelegramChatId != 0 {
		log.Printf(au.Gray(12, "[INIT]").String()+" "+au.Magenta("Checking admin rights for bot in group chat ID: %d").String(), cfg.TelegramChatId)

		// Check admin rights

		ok, err := tb.CheckAndRequestAdminRights(cfg.TelegramChatId)
		if err != nil {
			log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red("Error during CheckAndRequestAdminRights API call: %v").String(), err)
		} else if !ok {
			if err := tb.SendMessage("For correct operation, I need administrator rights in this group chat. Please provide them."); err != nil {
				log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red("Failed to send admin rights request message to chat %d: %v").String(), cfg.TelegramChatId, err)
			}
			log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red("Failed admin rights for chat: %d").String(), cfg.TelegramChatId)
		}

		// Check topics enabled

		ok, err = tb.CheckTopicsEnabled(cfg.TelegramChatId)
		if err != nil {
			log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red("Error checking topics for chat ID %d: %v").String(), cfg.TelegramChatId, err)
		} else if !ok {
			if err := tb.SendMessage("Topics are not enabled in this group. Please enable them for proper functionality."); err != nil {
				log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red("Error sending 'topics not enabled' notification to chat ID %d: %v").String(), cfg.TelegramChatId, err)
			}
			log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red("Failed topics enabled for chat: %d").String(), cfg.TelegramChatId)
		}
	}

	// OpenAI Client init

	var ai *OpenAIClient
	ai, err = NewOpenAIClient(cfg.OpenAIToken)
	if err != nil {
		log.Printf(au.Gray(12, "[INIT]").String()+" "+au.Yellow(aurora.Bold("Warning: Failed to initialize OpenAI client: %v. OpenAI features will be disabled.")).String(), err)
		ai = nil
	} else if ai == nil {
		log.Println(au.Gray(12, "[INIT]").String() + " " + au.Yellow("OpenAI token not provided or empty. OpenAI features will be disabled.").String())
	} else {
		log.Println(au.Gray(12, "[INIT]").String() + " " + au.Green("OpenAI client initialized successfully.").String())
	}

	// User request for username if needed

	// TODO: test for multiple user wrong input
	email, password := cfg.GetCred()
	for email == "" {
		email, err = tb.RequestUserInput("Enter your email please...")
		if err != nil {
			log.Printf(au.Gray(12, "[INIT]").String()+" "+au.Red("Error getting username: %v").String(), err)
			continue
		}
		if _, err := mail.ParseAddress(email); err != nil {
			if err := tb.SendMessage("Email not valid!"); err != nil {
				log.Printf(au.Gray(12, "[INIT]").String()+" "+au.Red("Failed to send 'Email not valid' message: %v").String(), err)
			}
			email = ""
			continue
		}
		email = strings.ToLower(email)
		cfg.SetCred(email, password)
	}

	// User request for password if needed

	// TODO: test for multiple user wrong input
	for password == "" {
		password, err = tb.RequestUserInput(fmt.Sprintf("Enter your password for %s, please...", email))
		if err != nil {
			log.Printf(au.Gray(12, "[INIT]").String()+" "+au.Red("Error getting username: %v").String(), err)
			continue
		}
		imap.RetryCount = 0
		c, err := imap.New(email, password, cfg.EmailImapHost, cfg.EmailImapPort)
		if err != nil {
			log.Printf(au.Gray(12, "[INIT]").String()+" "+au.Red("Failed to login to server: %v").String(), err)
			if err := tb.SendMessage("Wrong password!"); err != nil {
				log.Printf(au.Gray(12, "[INIT]").String()+" "+au.Red("Failed to send 'Wrong password' message: %v").String(), err)
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
			processNewEmails(emailClient, tb, ai)
		})
	if err != nil {
		log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red(aurora.Bold("Failed to init email client: %v")).String(), err)
	}
	defer emailClient.Close()

	// Telegram listener

	go tb.StartListener(
		func(uid int, message string, files []struct{ Url, Name string }) {
			replayToEmail(emailClient, tb, uid, message, files)
		},
		func(to string, title string, message string, files []struct{ Url, Name string }) {
			sendNewEmail(emailClient, tb, to, title, message, files)
		},
	)

	processNewEmails(emailClient, tb, ai)

	// Graceful shutdown

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	// Waiting signal OS

	<-signalChan
	log.Println(au.Gray(12, "[END]").String() + " " + au.Yellow("Shutdown signal received").String())
}

var mu sync.Mutex

func processNewEmails(ec *EmailClient, tb *TelegramBot, ai *OpenAIClient) {

	// Stopping idle mode

	mu.Lock()
	ec.imap.StopIdle()
	defer func() {
		if err := ec.startIdleWithHandler(); err != nil {
			tb.SendMessage("Failed to reply email for!")
			return
		}
		mu.Unlock()
	}()

	// Get new mail ids

	log.Println(au.Gray(12, "[EMAIL]").String() + " " + au.Cyan("Checking for new emails...").String())
	uids, err := ec.ListNewMailUIDs()
	if err != nil {
		log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Red("Error listing emails: %v").String(), err)
		return
	}

	// If first star, ignore all letters

	if uids, err = ec.AddAllUIDsIfFirstStart(uids); err != nil {
		log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Red("Error marking UIDs as processed on first start: %v").String(), err)
		return
	}

	// Main cycle for new letters

	for _, uid := range uids {
		m, err := ec.FetchMail(uid)
		if err != nil {
			log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Red("Error fetching email %d: %v").String(), uid, err)
			continue
		}
		d := ParseEmail(m, uid)

		isCode := false
		isSpam := false
		if ai != nil && d != nil && d.TextBody != "" {
			log.Printf(au.Gray(12, "[OPENAI]").String()+" "+au.Magenta("Attempting to process email UID %d with OpenAI...").String(), uid)
			res, err := ai.GenerateTextFromEmail(d.Subject + " " + d.From + " " + d.TextBody)
			if err != nil {
				log.Printf(au.Gray(12, "[OPENAI]").String()+" "+au.Red("Failed to process email UID %d with OpenAI: %v. Sending original email.").String(), uid, err)
			}
			isSpam = res.IsSpam
		}

		if err := tb.SendEmailData(d, isCode, isSpam); err != nil {
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Red("Error sending email %d to Telegram: %v").String(), uid, err)
			continue
		}
		if err := ec.MarkUIDAsProcessed(uid); err != nil {
			log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Red("Error marking email %d as processed: %v").String(), uid, err)
		}
	}

}

func replayToEmail(ec *EmailClient, tb *TelegramBot, uid int, msg string, files []struct{ Url, Name string }) {

	mu.Lock()
	ec.imap.StopIdle()
	err := ec.ReplyTo(uid, msg, files)
	if err != nil {
		tb.SendMessage("Failed to reply email for!")
	}
	if err := ec.startIdleWithHandler(); err != nil {
		log.Fatalf(au.Gray(12, "[EMAIL]").String()+" "+au.Red(aurora.Bold("Failed to restart idle mode: %v")).String(), err)
	}
	mu.Unlock()

}

func sendNewEmail(ec *EmailClient, tb *TelegramBot, to, subj, msg string, files []struct{ Url, Name string }) {

	mu.Lock()
	ec.imap.StopIdle()
	err := ec.SendMail([]string{to}, subj, msg, files)
	if err != nil {
		tb.SendMessage("Failed to send email!")
	}
	if err := ec.startIdleWithHandler(); err != nil {
		log.Fatalf(au.Gray(12, "[EMAIL]").String()+" "+au.Red(aurora.Bold("Failed to restart idle mode: %v")).String(), err)
	}
	mu.Unlock()

}
