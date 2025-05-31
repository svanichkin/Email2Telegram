package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type TelegramBot struct {
	api           *tgbotapi.BotAPI
	allowedUserID int64
}

func NewTelegramBot(apiToken string, allowedUserID int64) (*TelegramBot, error) {

	bot, err := tgbotapi.NewBotAPI(apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create Telegram bot API: %w", err)
	}
	bot.Debug = true // Enable debug mode for more verbose output
	log.Printf("Authorized on account %s", bot.Self.UserName)

	return &TelegramBot{api: bot, allowedUserID: allowedUserID}, nil
}

func (tb *TelegramBot) RequestPassword(prompt string) (string, error) {

	// Send message for user

	if tb.api == nil {
		return "", errors.New("telegram API is not initialized")
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

const maxLen = 4000

func (tb *TelegramBot) SendEmailData(data *ParsedEmailData) error {

	if tb.api == nil {
		return errors.New("telegram API is not initialized")
	}
	if data == nil {
		return errors.New("parsed email data is nil")
	}

	var cumulativeError error

	var messages []string
	text := "<b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>" + "\n\n" + data.TextBody
	messages = splitHTML(text)

	log.Printf("Attempting to send main email message (Subject: %s) to chat ID %d", data.Subject, tb.allowedUserID)
	for i := range len(messages) {
		msg := tgbotapi.NewMessage(tb.allowedUserID, messages[i])
		msg.ParseMode = "HTML"
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

func splitHTML(text string) []string {

	var blocks []string
	for len(text) > 0 {
		if len(text) < maxLen {
			blocks = append(blocks, text)
			break
		}
		cut := cutTextBeforePos(text, maxLen)
		cut, pos, open := findCutPoint(cut)
		if cut == "" {
			blocks = append(blocks, cutTextBeforePos(text, maxLen))
			text = cutTextAfterPos(text, maxLen)
		} else {
			blocks = append(blocks, cut)
			text = open + cutTextAfterPos(text, pos)
		}
	}

	return blocks
}

// Get text maxLen, then find cut point
func findCutPoint(cut string) (string, int, string) {

	positions := []int{
		findLastTagPosition(cut, "</a>", "\n"),
		findLastTagPosition(cut, "</b>", ""),
		findLastTagPosition(cut, "</code>", ""),
		findLastTagPosition(cut, "</i>", ""),
		findLastTagPosition(cut, "</pre>", ""),
		findLastTagPosition(cut, "</s>", ""),
		findLastTagPosition(cut, "</u>", ""),
	}
	maxPos := -1
	for _, pos := range positions {
		if pos > maxPos {
			maxPos = pos
		}
	}
	nPos := findLastTagPosition(cut, "\n", "")
	if nPos == -1 {
		nPos = findLastPrefixLeftPosition(cut, "<a href=")
	}
	if maxPos < nPos {
		maxPos = nPos
	}
	cut = cutTextBeforePos(cut, maxPos)
	open, close := findEnclosingTags(cut, nPos)
	if len(close) > 0 {
		cut = cut + close
	}

	return cut, maxPos, open
}

func cutTextBeforePos(text string, pos int) string {

	if pos > len(text) {
		pos = len(text)
	}
	if pos < 0 {
		pos = 0
	}

	return text[:pos]
}

func cutTextAfterPos(text string, pos int) string {

	if pos < 0 {
		pos = 0
	}
	if pos > len(text) {
		pos = len(text)
	}

	return text[pos:]
}

func findLastTagPosition(text, prefix, postfix string) int {

	lastPos := -1
	offset := 0
	for {
		idx := strings.Index(text[offset:], prefix)
		if idx == -1 {
			break
		}
		absoluteIdx := offset + idx
		pos := absoluteIdx + len(prefix)

		hasPostfix := false
		if postfix == "" {
			hasPostfix = true
		} else if pos <= len(text)-len(postfix) && strings.HasPrefix(text[pos:], postfix) {
			hasPostfix = true
		}

		if hasPostfix {
			lastPos = pos
		}
		offset = absoluteIdx + 1
	}

	return lastPos
}

func findLastPrefixLeftPosition(text, prefix string) int {

	lastPos := -1
	offset := 0
	for {
		idx := strings.Index(text[offset:], prefix)
		if idx == -1 {
			break
		}
		absoluteIdx := offset + idx
		lastPos = absoluteIdx
		offset = absoluteIdx + 1 // ищем все вхождения, даже перекрывающиеся
	}

	return lastPos
}

func findEnclosingTags(text string, pos int) (string, string) {

	if pos > len(text) {
		pos = len(text)
	}
	i := pos - 1
	for i >= 0 {
		if text[i] == '<' {
			// search start tag
			end := i + 1
			for end < len(text) && text[end] != '>' {
				end++
			}
			if end >= len(text) {
				break
			}
			tagContent := text[i+1 : end]
			tagParts := strings.Fields(tagContent)
			if len(tagParts) == 0 {
				break
			}
			tagName := tagParts[0]
			// if closed tag - skip
			if strings.HasPrefix(tagName, "/") {
				return "", ""
			}
			tagNameClean := strings.Split(tagName, " ")[0]
			openTag := "<" + tagName + ">"
			closeTag := "</" + tagNameClean + ">"
			return openTag, closeTag
		}
		i--
	}

	return "", ""
}
