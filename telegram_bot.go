package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/svanichkin/TelegramHTML"
)

type TelegramBot struct {
	api           *tgbotapi.BotAPI
	allowedUserID int64
	token         string
}

func NewTelegramBot(apiToken string, allowedUserID int64) (*TelegramBot, error) {

	bot, err := tgbotapi.NewBotAPI(apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create Telegram bot API: %w", err)
	}
	// bot.Debug = true // Enable debug mode for more verbose output
	log.Printf("Authorized on account %s", bot.Self.UserName)

	return &TelegramBot{api: bot, allowedUserID: allowedUserID, token: apiToken}, nil
}

func (t *TelegramBot) StartListener(
	replayMessage func(uid int, message string, files []struct{ Url, Name string }),
	newMessage func(to string, title string, message string, files []struct{ Url, Name string }),
) {

	updates := t.api.GetUpdatesChan(tgbotapi.UpdateConfig{
		Timeout: 60,
	})

	for update := range updates {
		t.handleUpdate(update, replayMessage, newMessage)
	}
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

func (tb *TelegramBot) SendEmailData(data *ParsedEmailData) error {

	if tb.api == nil {
		return errors.New("telegram API is not initialized")
	}
	if data == nil {
		return errors.New("parsed email data is nil")
	}

	var cumulativeError error

	// Header + text, then split

	var messages []string
	text := "<b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>" + "\n\n<a href=\"https://t.me/email_redirect_bot?start=msg12345\">Открыть сообщение</a>\n\n" + data.TextBody
	messages = telehtml.SplitTelegramHTML(text)

	// Send messages

	log.Printf("Attempting to send main email message (Subject: %s) to chat ID %d", data.Subject, tb.allowedUserID)
	for i := range len(messages) {
		msg := tgbotapi.NewMessage(tb.allowedUserID, messages[i]+telehtml.EncodeIntInvisible(data.Uid))
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

	// Other Attachments

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
					cumulativeError = fmt.Errorf("%v; failed to send attachment %s: %w", cumulativeError, filename, err)
				}
			}
		}
	}

	return cumulativeError
}

// Events from user

type FileAttachment struct {
	Name string
	Mime string
	Data []byte
}

func (t *TelegramBot) getFileURL(fileID string) string {
	file, _ := t.api.GetFile(tgbotapi.FileConfig{FileID: fileID})
	return file.Link(t.token)
}

func (t *TelegramBot) getAllFileURLs(msg *tgbotapi.Message) []struct{ Url, Name string } {
	files := []struct{ Url, Name string }{}

	if msg.Document != nil {
		if url := t.getFileURL(msg.Document.FileID); url != "" {
			files = append(files, struct{ Url, Name string }{url, msg.Document.FileName})
		}
	}
	if msg.Audio != nil {
		if url := t.getFileURL(msg.Audio.FileID); url != "" {
			files = append(files, struct{ Url, Name string }{url, "audio.mp3"})
		}
	}
	if msg.Video != nil {
		if url := t.getFileURL(msg.Video.FileID); url != "" {
			files = append(files, struct{ Url, Name string }{url, "video.mp4"})
		}
	}
	if msg.Voice != nil {
		if url := t.getFileURL(msg.Voice.FileID); url != "" {
			files = append(files, struct{ Url, Name string }{url, "voice.ogg"})
		}
	}
	if msg.Animation != nil {
		if url := t.getFileURL(msg.Animation.FileID); url != "" {
			files = append(files, struct{ Url, Name string }{url, "animation.mp4"})
		}
	}
	if msg.VideoNote != nil {
		if url := t.getFileURL(msg.VideoNote.FileID); url != "" {
			files = append(files, struct{ Url, Name string }{url, "video_note.mp4"})
		}
	}
	if len(msg.Photo) > 0 {
		photo := msg.Photo[len(msg.Photo)-1]
		if url := t.getFileURL(photo.FileID); url != "" {
			files = append(files, struct{ Url, Name string }{url, "photo.jpg"})
		}
	}

	return files
}

func (t *TelegramBot) handleUpdate(
	update tgbotapi.Update,
	replyMessage func(uid int, message string, files []struct{ Url, Name string }),
	newMessage func(to string, title string, message string, files []struct{ Url, Name string }),
) {

	if update.Message == nil {
		return
	}
	msg := update.Message
	if msg.From.ID != t.allowedUserID {
		return
	}

	// Reply message

	if msg.ReplyToMessage != nil {
		repliedText := msg.ReplyToMessage.Text
		if uidCode := telehtml.FindInvisibleIntSequences(repliedText); len(uidCode) > 0 {

			// Group files

			if t.bufferAlbumMessage(msg, func(msgs []*tgbotapi.Message) {
				files := []struct{ Url, Name string }{}
				for _, m := range msgs {
					files = append(files, t.getAllFileURLs(m)...)
				}
				replyMessage(telehtml.DecodeIntInvisible(uidCode[0]), extractTextFromMessages(msgs), files)
			}) {
				return
			}

			// Single file

			files := t.getAllFileURLs(msg)
			body := msg.Text
			if body == "" {
				body = msg.Caption
			}
			replyMessage(telehtml.DecodeIntInvisible(uidCode[0]), body, files)
		}
		log.Printf("Reply message received: %s", msg.Text)
		return
	}

	// Group files

	if t.bufferAlbumMessage(msg, func(msgs []*tgbotapi.Message) {
		var rawText string
		for _, m := range msgs {
			if m.Text != "" {
				rawText = m.Text
				break
			}
			if m.Caption != "" {
				rawText = m.Caption
				break
			}
		}
		to, title, body, ok := parseMailContent(rawText)
		if !ok {
			log.Println("Invalid mail format in album")
			return
		}
		files := []struct{ Url, Name string }{}
		for _, m := range msgs {
			files = append(files, t.getAllFileURLs(m)...)
		}
		log.Println("Final after album:", files)
		newMessage(to, title, body, files)
	}) {
		return
	}

	// Single file

	msgText := msg.Text
	if msgText == "" {
		msgText = msg.Caption
	}
	to, title, body, ok := parseMailContent(msgText)
	if !ok {
		log.Println("Invalid mail format in single message")
		return
	}
	files := t.getAllFileURLs(msg)
	newMessage(to, title, body, files)
}

func extractTextFromMessages(msgs []*tgbotapi.Message) string {
	for _, m := range msgs {
		if m.Text != "" {
			return m.Text
		}
		if m.Caption != "" {
			return m.Caption
		}
	}
	return ""
}

func parseMailContent(msgText string) (to, title, body string, ok bool) {

	firstNL := strings.Index(msgText, "\n")
	if firstNL == -1 {
		return
	}
	to = strings.TrimSpace(msgText[:firstNL])
	if !strings.Contains(to, "@") {
		return
	}
	rest := msgText[firstNL+1:]
	secondNL := strings.Index(rest, "\n")
	if secondNL == -1 {
		return
	}
	title = strings.TrimSpace(rest[:secondNL])
	if len(title) == 0 {
		return
	}
	body = rest[secondNL+1:]
	ok = true

	return
}

func (t *TelegramBot) bufferAlbumMessage(msg *tgbotapi.Message, callback func([]*tgbotapi.Message)) bool {

	if msg.MediaGroupID == "" {
		return false
	}

	albumLock.Lock()
	defer albumLock.Unlock()

	entry, exists := albumBuffer[msg.MediaGroupID]
	if !exists {
		entry = &albumEntry{}
		albumBuffer[msg.MediaGroupID] = entry
	}

	entry.messages = append(entry.messages, msg)

	if entry.timer != nil {
		entry.timer.Stop()
	}

	entry.timer = time.AfterFunc(1*time.Second, func() {
		albumLock.Lock()
		defer albumLock.Unlock()

		messages := albumBuffer[msg.MediaGroupID].messages
		delete(albumBuffer, msg.MediaGroupID)

		callback(messages)
	})

	return true
}

type albumEntry struct {
	messages []*tgbotapi.Message
	timer    *time.Timer
}

var albumBuffer = make(map[string]*albumEntry)
var albumLock sync.Mutex
