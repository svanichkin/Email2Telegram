package main

import (
	"fmt"
	"log"
	"net/mail"
	"os"
	"os/signal"
	"syscall"

	"github.com/svanichkin/go-imap"
)

func main() {

	// return

	log.Println("Starting Email Processor...")

	// Config loading

	cfg, err := LoadConfig("config.ini")
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

	emailClient, err := NewEmailClient(cfg.EmailImapHost, cfg.EmailImapPort, cfg.EmailSmtpHost, cfg.EmailSmtpPort, email, password)
	if err != nil {
		log.Fatalf("Failed to init email client: %v", err)
	}
	defer emailClient.Close()

	// Telegram listener

	go telegramBot.StartListener(
		func(uid int, message string, files []struct{ Url, Name string }) {
			emailClient.ReplyTo(uid, message, files)
		},
		func(to, title, message string, files []struct{ Url, Name string }) {
			emailClient.SendMail([]string{to}, title, message, files)
		},
	)

	// Graceful shutdown

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	err = emailClient.RunUpdateChecker(func() {
		processNewEmails(emailClient, telegramBot)
	})
	if err != nil {
		log.Fatalf("Email client error: %v", err)
	}

	processNewEmails(emailClient, telegramBot)

	// Waiting signal OS

	<-signalChan
	log.Println("Shutdown signal received")
}

func processNewEmails(emailClient *EmailClient, telegramBot *TelegramBot) {

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
