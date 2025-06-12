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

	"github.com/svanichkin/go-imap"
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
			processNewEmails(emailClient, telegramBot)
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
		func(to, title, message string, files []struct{ Url, Name string }) {
			sendNewEmail(emailClient, telegramBot, to, title, message, files)
		},
	)

	processNewEmails(emailClient, telegramBot)

	// Graceful shutdown

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	// Waiting signal OS

	<-signalChan
	log.Println("Shutdown signal received")
}

var mu sync.Mutex

func processNewEmails(emailClient *EmailClient, telegramBot *TelegramBot) {

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
		if err := telegramBot.SendEmailData(ParseEmail(mail, uid)); err != nil {
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
