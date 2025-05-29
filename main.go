package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.Println("Starting Email Processor...")

	// Остальная часть main (конфигурация, IMAP и т.д.)
	cfg, err := LoadConfig("config.ini")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Инициализация Telegram бота
	telegramBot, err := NewTelegramBot(cfg.TelegramToken, cfg.TelegramUserID)
	if err != nil {
		log.Fatalf("Failed to init Telegram bot: %v", err)
	}

	// Получение пароля от почты
	emailPassword, err := telegramBot.RequestPassword(fmt.Sprintf("Enter password for email %s:", cfg.EmailUsername))
	if err != nil {
		log.Fatalf("Failed to get password: %v", err)
	}

	// Инициализация почтового клиента
	emailClient, err := NewEmailClient(cfg.EmailHost, cfg.EmailPort, cfg.EmailUsername, emailPassword)
	if err != nil {
		log.Fatalf("Failed to init email client: %v", err)
	}
	defer emailClient.Close()

	// Обработка graceful shutdown
	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, os.Interrupt, syscall.SIGTERM)

	// Основной цикл обработки
	ticker := time.NewTicker(time.Duration(cfg.CheckIntervalSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			processNewEmails(emailClient, telegramBot)
		case <-shutdownChan:
			log.Println("Shutting down...")
			return
		}
	}
}

func processNewEmails(emailClient *EmailClient, telegramBot *TelegramBot) {
	log.Println("Checking for new emails...")
	uids, err := emailClient.ListNewMailUIDs()
	if err != nil {
		log.Printf("Error listing emails: %v", err)
		return
	}

	for _, uid := range uids {
		_, bytes, err := emailClient.FetchMail(uid)
		if err != nil {
			log.Printf("Error fetching email %d: %v", uid, err)
			continue
		}

		parsedData, err := ParseEmail(bytes)
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
