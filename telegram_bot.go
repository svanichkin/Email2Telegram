package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	telehtml "github.com/svanichkin/TelegramHTML"
)

type TelegramBot struct {
	api         *tgbotapi.BotAPI
	recipientId int64
	token       string
	updates     tgbotapi.UpdatesChannel
	isChat      bool
	topics      map[string]string
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

	return &TelegramBot{
		api:         bot,
		recipientId: recipientID,
		token:       apiToken,
		updates:     updates,
	}, nil
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
	tb.api.Send(tgbotapi.NewMessage(tb.recipientId, msg))
}

func (tb *TelegramBot) RequestUserInput(prompt string) (string, error) {
	if tb.api == nil {
		return "", errors.New("telegram API is not initialized")
	}

	msg := tgbotapi.NewMessage(tb.recipientId, prompt)
	if _, err := tb.api.Send(msg); err != nil {
		return "", fmt.Errorf("failed to send: %w", err)
	}
	log.Printf("Sent prompt to recipient ID %d: %s", tb.recipientId, prompt)

	timeout := time.After(5 * time.Minute)

	for {
		select {
		case update := <-tb.updates:
			if update.Message == nil {
				continue
			}
			if update.Message.Chat.ID == tb.recipientId {
				log.Printf("Received reply: %s", update.Message.Text)
				return update.Message.Text, nil
			}
			log.Printf("RequestUserInput: Ignored message in chat %d from user %d (expected chat %d)", update.Message.Chat.ID, update.Message.From.ID, tb.recipientId)

		case <-timeout:
			log.Println("Timeout waiting for input reply.")
			return "", errors.New("timeout waiting for input reply")
		}
	}
}

func (tb *TelegramBot) SendEmailData(data *ParsedEmailData, isCode, isSpam bool) error {

	if tb.api == nil {
		return errors.New("telegram API is not initialized")
	}
	if data == nil {
		return errors.New("parsed email data is nil")
	}

	var topicId string
	if tb.isChat && !isSpam {
		if tb.topics == nil {
			s := fmt.Sprint(tb.recipientId)
			tb.topics, _ = LoadAndDecrypt(s, s+".top")
		}
		s := cleanSubject(data.Subject)
		topicId = tb.topics[s]
		if topicId == "" {
			tb.topics = make(map[string]string)
			topicId, err := tb.ensureTopic(s)
			if err != nil {
				return fmt.Errorf("topic handling error: %w", err)
			}
			tb.topics[s] = fmt.Sprint(topicId)
			s := fmt.Sprint(tb.recipientId)
			EncryptAndSave(s, s+".top", tb.topics)
		}
	}

	// if code message

	if isCode {
		if !tb.isChat {
			t := "🔑 <b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>"
			msg := tgbotapi.NewMessage(tb.recipientId, t+telehtml.EncodeIntInvisible(data.Uid))
			msg.ParseMode = "HTML"
			msg.DisableWebPagePreview = true
			if _, err := tb.api.Send(msg); err != nil {
				return fmt.Errorf("failed to send title message: %w", err)
			}
			msg = tgbotapi.NewMessage(tb.recipientId, data.TextBody)
			if _, err := tb.api.Send(msg); err != nil {
				return fmt.Errorf("failed to send code message: %w", err)
			}
		} else {
			params := tgbotapi.Params{
				"chat_id":                  fmt.Sprint(tb.recipientId),
				"message_thread_id":        topicId,
				"text":                     data.TextBody,
				"parse_mode":               "HTML",
				"disable_web_page_preview": "true",
			}
			if _, err := tb.api.MakeRequest("sendMessage", params); err != nil {
				return fmt.Errorf("failed to send code message to topic: %w", err)
			}
		}
		return nil
	}

	// Header + text, then split

	var messages []string
	var t = data.TextBody
	if !tb.isChat {
		t = "<b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>" + "\n\n" + t
		if isSpam {
			t = "🚫 " + t
		} else {
			t = "✉️ " + t
		}
	}
	messages = telehtml.SplitTelegramHTML(t)

	// Send messages

	for i := range len(messages) {
		if !tb.isChat {
			msg := tgbotapi.NewMessage(tb.recipientId, messages[i]+telehtml.EncodeIntInvisible(data.Uid))
			msg.ParseMode = "HTML"
			msg.DisableWebPagePreview = true
			if _, err := tb.api.Send(msg); err != nil {
				return fmt.Errorf("failed to send main message: %w", err)
			}
		} else {
			params := tgbotapi.Params{
				"chat_id":                  fmt.Sprint(tb.recipientId),
				"message_thread_id":        topicId,
				"text":                     messages[i],
				"parse_mode":               "HTML",
				"disable_web_page_preview": "true",
			}
			if _, err := tb.api.MakeRequest("sendMessage", params); err != nil {
				return fmt.Errorf("failed to send code message to topic: %w", err)
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
			if tb.isChat {
				docMsg := tgbotapi.NewDocument(tb.recipientId, attachmentFile)
				log.Printf("Sending attachment: %s (size: %d bytes)", filename, len(contentBytes))
				if _, err := tb.api.Send(docMsg); err != nil {
					return fmt.Errorf("failed to send attachment %s: %w", filename, err)
				}
			} else {
				attachmentFile := tgbotapi.FileBytes{Name: filename, Bytes: contentBytes}
				body := &bytes.Buffer{}
				writer := multipart.NewWriter(body)
				writer.WriteField("chat_id", strconv.FormatInt(tb.recipientId, 10))
				writer.WriteField("message_thread_id", topicId)

				part, err := writer.CreateFormFile("document", filename)
				if err != nil {
					return fmt.Errorf("failed to create form file: %w", err)
				}
				if _, err := io.Copy(part, bytes.NewReader(attachmentFile.Bytes)); err != nil {
					return fmt.Errorf("failed to write file content: %w", err)
				}
				if err := writer.Close(); err != nil {
					return fmt.Errorf("failed to close multipart writer: %w", err)
				}
				apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", tb.api.Token)
				req, err := http.NewRequest("POST", apiURL, body)
				if err != nil {
					return fmt.Errorf("failed to create request: %w", err)
				}
				req.Header.Set("Content-Type", writer.FormDataContentType())
				client := &http.Client{}
				resp, err := client.Do(req)
				if err != nil {
					return fmt.Errorf("failed to send HTTP request: %w", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					respBody, _ := io.ReadAll(resp.Body)
					return fmt.Errorf("telegram API error: %d - %s", resp.StatusCode, string(respBody))
				}
				log.Printf("Successfully sent document to topic %d", topicId)
			}
		}
	}

	return nil
}

func cleanSubject(subject string) string {

	re := regexp.MustCompile(`(?i)^(re|fwd|fw):\s*`)
	for re.MatchString(subject) {
		subject = re.ReplaceAllString(subject, "")
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ""
	}
	return strings.ToUpper(subject)

}

func (tb *TelegramBot) ensureTopic(subject string) (int, error) {

	params := tgbotapi.Params{
		"chat_id":    fmt.Sprint(tb.recipientId),
		"name":       subject,
		"icon_color": "1", // сделать рандом Color of the topic icon in RGB format. Currently, must be one of 7322096 (0x6FB9F0), 16766590 (0xFFD67E), 13338331 (0xCB86DB), 9367192 (0x8EEE98), 16749490 (0xFF93B2), or 16478047 (0xFB6F5F)
	}

	resp, err := tb.api.MakeRequest("createForumTopic", params)
	if err != nil {
		return 0, fmt.Errorf("createForumTopic error: %w", err)
	}

	var createResult struct {
		Result struct {
			MessageThreadID int `json:"message_thread_id"`
		} `json:"result"`
	}

	if err := json.Unmarshal(resp.Result, &createResult); err != nil {
		return 0, err
	}

	return createResult.Result.MessageThreadID, nil
}

func (tb *TelegramBot) CheckAndRequestAdminRights(chatID int64) (bool, error) {

	if tb.api == nil {
		return false, errors.New("telegram API is not initialized in CheckAndRequestAdminRights")
	}

	// Get bot's own ID

	botID := tb.api.Self.ID

	// Get chat member information for the bot in the specified chat

	chatMember, err := tb.api.GetChatMember(tgbotapi.GetChatMemberConfig{
		ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
			ChatID: chatID,
			UserID: botID,
		},
	})
	if err != nil {
		return false, fmt.Errorf("failed to get chat member info for bot %d in chat %d: %w", botID, chatID, err)
	}

	// Check if the bot is an administrator or creator

	// Common statuses: "creator", "administrator", "member", "restricted", "left", "kicked"
	status := chatMember.Status
	log.Printf("Bot status in chat %d is: %s", chatID, status)

	if status != "administrator" && status != "creator" {
		return false, fmt.Errorf("sent admin rights request to chat %d", chatID)
	} else {
		log.Printf("Bot already has admin rights in chat %d (status: %s)", chatID, status)
	}

	return true, nil
}

func (tb *TelegramBot) CheckTopicsEnabled(chatID int64) (bool, error) {

	tb.isChat = false

	if tb.api == nil {
		return tb.isChat, errors.New("telegram API not initialized")
	}

	params := tgbotapi.Params{
		"chat_id": fmt.Sprint(chatID),
	}
	_, err := tb.api.MakeRequest("getForumTopicIconStickers", params)
	if err != nil {
		if strings.Contains(err.Error(), "the chat is not a forum") {
			return tb.isChat, nil
		}
		return tb.isChat, err
	}

	tb.isChat = true
	return tb.isChat, nil
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

	if msg.Chat == nil {
		log.Printf("Ignoring update with nil Chat: UpdateID %d", update.UpdateID)
		return
	}

	// The primary check: is the message from the configured chat/user?

	if msg.Chat.ID != t.recipientId {
		fromID := int64(0)
		if msg.From != nil {
			fromID = msg.From.ID
		}
		senderChatID := int64(0)
		if msg.SenderChat != nil {
			senderChatID = msg.SenderChat.ID
		}
		log.Printf("Ignoring message from unexpected chat: MessageChat.ID=%d, From.ID=%d, SenderChat.ID=%d. Expected recipientID: %d", msg.Chat.ID, fromID, senderChatID, t.recipientId)
		return
	}

	log.Printf("Processing message from Chat.ID %d (RecipientID: %d), From.ID %d", msg.Chat.ID, t.recipientId, msg.From.ID)

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
