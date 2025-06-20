package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	telehtml "github.com/svanichkin/TelegramHTML"
)

type TelegramBot struct {
	api         *tgbotapi.BotAPI
	recipientID int64
	token       string
	updates     tgbotapi.UpdatesChannel
}

func NewTelegramBot(apiToken string, recipientID int64) (*TelegramBot, error) {
	bot, err := tgbotapi.NewBotAPI(apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create Telegram bot API: %w", err)
	}
	log.Printf("Authorized on account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	return &TelegramBot{api: bot, recipientID: recipientID, token: apiToken, updates: updates}, nil
}

func (t *TelegramBot) StartListener(
	replayMessage func(uid int, message string, files []struct{ Url, Name string }),
	newMessage func(to string, title string, message string, files []struct{ Url, Name string }),
) {
	for update := range t.updates {
		t.handleUpdate(update, replayMessage, newMessage)
	}
}

func (tb *TelegramBot) SendMessage(msg string) {
	tb.api.Send(tgbotapi.NewMessage(tb.recipientID, msg))
}

func (tb *TelegramBot) RequestUserInput(prompt string) (string, error) {
	if tb.api == nil {
		return "", errors.New("telegram API is not initialized")
	}

	msg := tgbotapi.NewMessage(tb.recipientID, prompt)
	if _, err := tb.api.Send(msg); err != nil {
		return "", fmt.Errorf("failed to send: %w", err)
	}
	log.Printf("Sent prompt to recipient ID %d: %s", tb.recipientID, prompt)

	timeout := time.After(5 * time.Minute)

	for {
		select {
		case update := <-tb.updates:
			if update.Message == nil {
				continue
			}
			if update.Message.Chat.ID == tb.recipientID {
				log.Printf("Received reply: %s", update.Message.Text)
				return update.Message.Text, nil
			}
			log.Printf("RequestUserInput: Ignored message in chat %d from user %d (expected chat %d)", update.Message.Chat.ID, update.Message.From.ID, tb.recipientID)

		case <-timeout:
			log.Println("Timeout waiting for input reply.")
			return "", errors.New("timeout waiting for input reply")
		}
	}
}

func (tb *TelegramBot) SendEmailData(data *ParsedEmailData, isCode bool) error {

	if tb.api == nil {
		return errors.New("telegram API is not initialized")
	}
	if data == nil {
		return errors.New("parsed email data is nil")
	}

	var cumulativeError error

	// if code message

	var messages []string
	var text string
	if isCode {
		text = "<b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>"

		msg := tgbotapi.NewMessage(tb.recipientID, text+telehtml.EncodeIntInvisible(data.Uid))
		msg.ParseMode = "HTML"
		msg.DisableWebPagePreview = true
		if _, err := tb.api.Send(msg); err != nil {
			log.Printf("Error sending title message: %v", err)
			if cumulativeError == nil {
				cumulativeError = fmt.Errorf("failed to send title message: %w", err)
			} else {
				cumulativeError = fmt.Errorf("%v; failed to send title message: %w", cumulativeError, err)
			}
		}

		msg = tgbotapi.NewMessage(tb.recipientID, data.TextBody+telehtml.EncodeIntInvisible(data.Uid))
		if _, err := tb.api.Send(msg); err != nil {
			log.Printf("Error sending code message: %v", err)
			if cumulativeError == nil {
				cumulativeError = fmt.Errorf("failed to send code message: %w", err)
			} else {
				cumulativeError = fmt.Errorf("%v; failed to send code message: %w", cumulativeError, err)
			}
		}

		return cumulativeError

	}

	// Header + text, then split

	text = "<b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>" + "\n\n" + data.TextBody
	messages = telehtml.SplitTelegramHTML(text)

	// Send messages

	for i := range len(messages) {
		msg := tgbotapi.NewMessage(tb.recipientID, messages[i]+telehtml.EncodeIntInvisible(data.Uid))
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
		// log.Printf("Attempting to send %d other attachments to recipient ID %d", len(data.Attachments), tb.recipientID)
		for filename, contentBytes := range data.Attachments {
			if len(contentBytes) == 0 {
				log.Printf("Skipping attachment '%s' due to empty content.", filename)
				continue
			}
			attachmentFile := tgbotapi.FileBytes{Name: filename, Bytes: contentBytes}
			docMsg := tgbotapi.NewDocument(tb.recipientID, attachmentFile)
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
	replayMessage func(uid int, message string, files []struct{ Url, Name string }),
	newMessage func(to string, title string, message string, files []struct{ Url, Name string }),
) {

	if update.Message == nil {
		return
	}
	msg := update.Message

	if msg.Chat == nil { // Should not happen for normal messages
		log.Printf("Ignoring update with nil Chat: UpdateID %d", update.UpdateID)
		return
	}

	// The primary check: is the message from the configured chat/user?
	if msg.Chat.ID != t.recipientID {
		// Log details for diagnostics
		fromID := int64(0)
		if msg.From != nil {
			fromID = msg.From.ID
		}
		senderChatID := int64(0)
		if msg.SenderChat != nil {
			senderChatID = msg.SenderChat.ID
		}
		log.Printf("Ignoring message from unexpected chat: MessageChat.ID=%d, From.ID=%d, SenderChat.ID=%d. Expected recipientID: %d", msg.Chat.ID, fromID, senderChatID, t.recipientID)
		return
	}

	// At this point, the message is in the correct chat (either DM or the configured group).
	// The original issue implies that if chat_id is used, any user in that chat can interact.
	// If user_id was used for a DM, msg.Chat.ID == t.recipientID is sufficient.
	// No further From.ID check is strictly needed here based on the issue's requirements for group mode.
	// If cfg.TelegramUserId is set (even in group mode, for potential admin actions not covered here),
	// that check could be added *within* specific command handlers if needed, not as a global filter.

	log.Printf("Processing message from Chat.ID %d (RecipientID: %d), From.ID %d", msg.Chat.ID, t.recipientID, msg.From.ID)

	// Command handling removed from here

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
				replayMessage(telehtml.DecodeIntInvisible(uidCode[0]), extractTextFromMessages(msgs), files)
			}) {
				return
			}

			// Single file

			files := t.getAllFileURLs(msg)
			body := msg.Text
			if body == "" {
				body = msg.Caption
			}
			replayMessage(telehtml.DecodeIntInvisible(uidCode[0]), body, files)
		}

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
		t.SendMessage("Hi! I'm your mail bot.")
		t.SendMessage("To reply to an email, just reply to the message and \n\nenter your text, and attach files if needed.")
		t.SendMessage("To send a new email, use the format:\n\nto.user@mail.example.com\nSubject line\nEmail text\n\nAttach files if needed.")
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
