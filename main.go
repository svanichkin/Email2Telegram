package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/zalando/go-keyring"
)

func main() {
	log.Println("Starting Email Processor...")

	// Config loading

	cfg, err := LoadConfig("config.ini")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Telegram init

	telegramBot, err := NewTelegramBot(cfg.TelegramToken, cfg.TelegramUserID)
	if err != nil {
		log.Fatalf("Failed to init Telegram bot: %v", err)
	}
	go telegramBot.StartListener()

	// Password from uzer or keychain

	emailPassword, err := keyring.Get("email2Telegram", cfg.EmailUsername)
	if err != nil {
		emailPassword, err = telegramBot.RequestPassword(
			fmt.Sprintf("Enter password for email %s:", cfg.EmailUsername),
		)
		if err != nil {
			log.Fatalf("Failed to get password: %v", err)
		}
		if err := keyring.Set("email2Telegram", cfg.EmailUsername, emailPassword); err != nil {
			log.Printf("Warning: failed to save password to keyring: %v", err)
		} else {
			log.Println("Password saved to system keyring")
		}
	} else {
		log.Println("Password loaded from system keyring")
	}

	// Mail init

	emailClient, err := NewEmailClient(cfg.EmailHost, cfg.EmailPort, cfg.EmailUsername, emailPassword)
	if err != nil {
		log.Fatalf("Failed to init email client: %v", err)
	}
	defer emailClient.Close()

	// Graceful shutdown

	shutdownChan := make(chan struct{})
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		checkerFunc := func() {
			processNewEmails(emailClient, telegramBot)
		}
		err := emailClient.RunWithIdleAndTickerCallback(cfg.CheckIntervalSeconds, shutdownChan, checkerFunc)
		if err != nil {
			log.Fatalf("Email client error: %v", err)
		}
	}()

	// Waiting signal OS

	<-signalChan
	log.Println("Shutdown signal received")

	close(shutdownChan)
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
		_, bytes, err := emailClient.FetchMail(uid)
		if err != nil {
			log.Printf("Error fetching email %d: %v", uid, err)
			continue
		}
		parsedData, err := ParseEmail(bytes, uid)
		if err != nil {
			log.Printf("Error parsing email %d: %v", uid, err)
			continue
		}
		if err := telegramBot.SendEmailData(parsedData); err != nil {
			log.Printf("Error sending email %d to Telegram: %v", uid, err)
			continue
		}
		if err := emailClient.MarkUIDAsProcessed(uid); err != nil {
			log.Printf("Error marking email %d as processed: %v", uid, err)
		}
	}

}
