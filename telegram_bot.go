package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TelegramBot holds the bot API and the allowed user ID
type TelegramBot struct {
	api           *tgbotapi.BotAPI
	allowedUserID int64
}

// NewTelegramBot initializes and returns a new TelegramBot
func NewTelegramBot(apiToken string, allowedUserID int64) (*TelegramBot, error) {
	bot, err := tgbotapi.NewBotAPI(apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create Telegram bot API: %w", err)
	}
	// bot.Debug = true // Enable debug mode for more verbose output
	log.Printf("Authorized on account %s", bot.Self.UserName)

	return &TelegramBot{api: bot, allowedUserID: allowedUserID}, nil
}

// RequestPassword sends a prompt to the allowed user and waits for their reply.
func (tb *TelegramBot) RequestPassword(prompt string) (string, error) {
	if tb.api == nil {
		return "", errors.New("Telegram API is not initialized")
	}
	msg := tgbotapi.NewMessage(tb.allowedUserID, prompt)
	if _, err := tb.api.Send(msg); err != nil {
		return "", fmt.Errorf("failed to send password prompt: %w", err)
	}
	log.Printf("Sent password prompt to user ID %d: %s", tb.allowedUserID, prompt)

	// Set a timeout for waiting for the password
	timeout := time.After(5 * time.Minute)
	// Configure updates
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60 // Timeout for long polling

	updates := tb.api.GetUpdatesChan(u)

	log.Println("Waiting for password reply...")
	for {
		select {
		case update := <-updates:
			if update.Message == nil { // Ignore any non-Message updates
				continue
			}

			// Check if the message is from the allowed user and chat
			if update.Message.From.ID == tb.allowedUserID && update.Message.Chat.ID == tb.allowedUserID {
				log.Printf("Received reply from user ID %d: %s", tb.allowedUserID, update.Message.Text)
				// Clear any remaining updates in the channel to prevent processing old messages next time.
				for len(updates) > 0 {
					<-updates
				}
				return update.Message.Text, nil
			}
			log.Printf("Ignoring message from unexpected user ID %d or chat ID %d", update.Message.From.ID, update.Message.Chat.ID)

		case <-timeout:
			log.Println("Timeout waiting for password reply.")
			return "", errors.New("timeout waiting for password reply")
		}
	}
}

const maxLen = 3800

// SendEmailData sends the parsed email data to the configured user.
func (tb *TelegramBot) SendEmailData(data *ParsedEmailData) error {
	if tb.api == nil {
		return errors.New("telegram API is not initialized")
	}
	if data == nil {
		return errors.New("parsed email data is nil")
	}

	var cumulativeError error

	// 1. Main Message (Subject + Text Body)
	// безопасный лимит
	var messages []string
	text := fmt.Sprintf("*From:* %s\n*To:* %s\n\n*%s*\n\n%s", data.From, data.To, data.Subject, data.TextBody)
	messages = SplitText(text)
	// for len(text) > 0 {
	// 	if len(text) > maxLen {
	// 		messages = append(messages, text[:maxLen])
	// 		text = text[maxLen:]
	// 	} else {
	// 		messages = append(messages, text)
	// 		break
	// 	}
	// }

	log.Printf("Attempting to send main email message (Subject: %s) to chat ID %d", data.Subject, tb.allowedUserID)
	for i := range len(messages) {
		msg := tgbotapi.NewMessage(tb.allowedUserID, messages[i])
		msg.ParseMode = "Markdown"
		msg.DisableWebPagePreview = true
		if _, err := tb.api.Send(msg); err != nil {
			log.Printf("Error sending main message: %v", err)
			if cumulativeError == nil {
				cumulativeError = fmt.Errorf("failed to send main message: %w", err)
			} else {
				cumulativeError = fmt.Errorf("%v; failed to send main message: %w", cumulativeError, err)
			}
		}
	}

	// 3. Other Attachments
	if len(data.Attachments) > 0 {
		log.Printf("Attempting to send %d other attachments to chat ID %d", len(data.Attachments), tb.allowedUserID)
		for filename, contentBytes := range data.Attachments {
			if len(contentBytes) == 0 {
				log.Printf("Skipping attachment '%s' due to empty content.", filename)
				continue
			}
			attachmentFile := tgbotapi.FileBytes{Name: filename, Bytes: contentBytes}
			docMsg := tgbotapi.NewDocument(tb.allowedUserID, attachmentFile)
			log.Printf("Sending attachment: %s (size: %d bytes)", filename, len(contentBytes))
			if _, err := tb.api.Send(docMsg); err != nil {
				log.Printf("Error sending attachment '%s': %v", filename, err)
				if cumulativeError == nil {
					cumulativeError = fmt.Errorf("failed to send attachment %s: %w", filename, err)
				} else {
					cumulativeError = fmt.Errorf("%v; failed to send attachment %s: %w", cumulativeError, err)
				}
			}
		}
	}

	return cumulativeError
}

func SplitText(text string) []string {
	var messages []string
	lines := strings.Split(text, "\n") // Делим текст на строки
	currentBlock := ""

	for _, line := range lines {
		// Пробуем добавить текущую строку к блоку
		proposedBlock := currentBlock
		if proposedBlock != "" {
			proposedBlock += "\n" // Добавляем перенос между строками
		}
		proposedBlock += line

		// Если блок превысил лимит
		if len(proposedBlock) > maxLen {
			if currentBlock == "" {
				// Экстренный случай: одна строка длиннее maxLen
				messages = append(messages, splitLongLine(line, maxLen)...)
			} else {
				// Сохраняем текущий блок и начинаем новый с этой строки
				messages = append(messages, currentBlock)
				currentBlock = line
			}
		} else {
			// Блок в пределах лимита - сохраняем изменения
			currentBlock = proposedBlock
		}
	}

	// Добавляем последний блок
	if currentBlock != "" {
		messages = append(messages, currentBlock)
	}
	return messages
}

func splitLongLine(line string, maxLen int) []string {
	var parts []string
	for len(line) > 0 {
		if len(line) <= maxLen {
			parts = append(parts, line)
			break
		}

		// Ищем безопасное место для разрыва (последний пробел перед лимитом)
		splitAt := strings.LastIndex(line[:maxLen], " ")
		if splitAt <= 0 {
			splitAt = maxLen // Если пробелов нет - делим по лимиту
		}

		parts = append(parts, line[:splitAt])
		line = line[splitAt:]
	}
	return parts
}
