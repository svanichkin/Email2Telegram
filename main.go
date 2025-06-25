package main

import (
	"context"
	"fmt"
	"log"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"strings"

	"github.com/BrianLeishman/go-imap"
	"github.com/logrusorgru/aurora/v4"
	"github.com/mymmrac/telego"
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

	// Telegram init

	tb, err := NewTelegramBot(cfg.TelegramToken, cfg.TelegramRecipientId)
	if err != nil {
		log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red(aurora.Bold("Failed to init Telegram bot: %v")).String(), err)
	}

	// Check permissions if group mode

	chat, err := tb.api.GetChat(context.Background(), &telego.GetChatParams{ChatID: telego.ChatID{ID: cfg.TelegramRecipientId}})
	if err != nil {
		log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red(aurora.Bold("Failed to get chat info Telegram bot: %v")).String(), err)
	}

	if chat.Type == "supergroup" {

		// Check topics enabled

		ok, err := tb.CheckTopicsEnabled(chat)
		if err != nil {
			log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red("Error checking topics for chat ID %d: %v").String(), cfg.TelegramRecipientId, err)
		} else if !ok {
			if err := tb.SendMessage("Topics are not enabled in this group. Please enable them for proper functionality."); err != nil {
				log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red("Error sending 'topics not enabled' notification to chat ID %d: %v").String(), cfg.TelegramRecipientId, err)
			}
			log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red("Failed topics enabled for chat: %d").String(), cfg.TelegramRecipientId)
		}

		// Check admin rights

		ok, err = tb.CheckAndRequestAdminRights(cfg.TelegramRecipientId)
		if err != nil {
			log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red("Error during CheckAndRequestAdminRights API call: %v").String(), err)
		} else if !ok {
			if err := tb.SendMessage("For correct operation, I need administrator rights in this group chat. Please provide them."); err != nil {
				log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red("Failed to send admin rights request message to chat %d: %v").String(), cfg.TelegramRecipientId, err)
			}
			log.Fatalf(au.Gray(12, "[INIT]").String()+" "+au.Red("Failed admin rights for chat: %d").String(), cfg.TelegramRecipientId)
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
		func(uid, tid int) {
			expandEmail(emailClient, tb, uid, tid)
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
