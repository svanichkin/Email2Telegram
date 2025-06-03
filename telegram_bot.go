package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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
	text := "<b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>" + "\n\n" + data.TextBody
	messages = splitHTML(text)

	// Send messages

	log.Printf("Attempting to send main email message (Subject: %s) to chat ID %d", data.Subject, tb.allowedUserID)
	for i := range len(messages) {
		msg := tgbotapi.NewMessage(tb.allowedUserID, messages[i]+encodeUidInvisible(data.Uid))
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

// Magic HTML splitter

const maxLen = 4000

func splitHTML(text string) []string {

	var blocks []string
	for len(text) > 0 {

		// If text length small - no need split

		if len(text) < maxLen {
			blocks = append(blocks, text)
			break
		}

		// Cut text

		cut := cutTextBeforePos(text, maxLen)

		//  Find best cut point

		cut, pos, open := findCutPoint(cut)

		// Add completed message block

		blocks = append(blocks, cut)

		// Open closed tag, if needed and cut text

		text = open + cutTextAfterPos(text, pos)
	}

	return blocks
}

func findCutPoint(cut string) (string, int, string) {

	// Find last cut point with tag

	positions := []int{
		findLastTagRightPosition(cut, "</a>", "\n"),
		findLastTagRightPosition(cut, "</b>", ""),
		findLastTagRightPosition(cut, "</code>", ""),
		findLastTagRightPosition(cut, "</i>", ""),
		findLastTagRightPosition(cut, "</pre>", ""),
		findLastTagRightPosition(cut, "</s>", ""),
		findLastTagRightPosition(cut, "</u>", ""),
		findLastTagRightPosition(cut, "\n", ""),
	}
	maxPos := -1
	for _, pos := range positions {
		if pos > maxPos {
			maxPos = pos
		}
	}

	// if notfound, cut with bad point

	if maxPos == -1 {
		maxPos = findLastTagLeftPosition(cut, "<a href=")
	}
	if maxPos == -1 {
		maxPos = findLastTagRightPosition(cut, ". ", "")
	}
	if maxPos == -1 {
		maxPos = findLastTagRightPosition(cut, ", ", "")
	}

	// if not found, cut with length

	if maxPos == -1 {
		maxPos = len(cut)
	}

	// Cut text

	cut = cutTextBeforePos(cut, maxPos)

	// Check for Close tag and close if needed

	open, close := findEnclosingTags(cut, maxPos)
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

func findLastTagRightPosition(text, prefix, postfix string) int {

	lastPos := -1
	offset := 0
	for {

		// Main work

		idx := strings.Index(text[offset:], prefix)
		if idx == -1 {
			break
		}
		absoluteIdx := offset + idx
		pos := absoluteIdx + len(prefix)

		// Postfix work

		hasPostfix := false
		if postfix == "" {
			hasPostfix = true
		} else if pos <= len(text)-len(postfix) && strings.HasPrefix(text[pos:], postfix) {
			hasPostfix = true
		}
		if hasPostfix {
			lastPos = pos
		}

		// New offset

		offset = absoluteIdx + 1
	}

	return lastPos
}

func findLastTagLeftPosition(text, prefix string) int {

	lastPos := -1
	offset := 0
	for {
		idx := strings.Index(text[offset:], prefix)
		if idx == -1 {
			break
		}
		absoluteIdx := offset + idx
		lastPos = absoluteIdx
		offset = absoluteIdx + 1
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

			// Search start tag

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

			// If closed tag - skip

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
		if uidCode := findInvisibleUidSequences(repliedText); len(uidCode) > 0 {

			// Group files

			if t.bufferAlbumMessage(msg, func(msgs []*tgbotapi.Message) {
				files := []struct{ Url, Name string }{}
				for _, m := range msgs {
					files = append(files, t.getAllFileURLs(m)...)
				}
				replyMessage(decodeUidInvisible(uidCode[0]), extractTextFromMessages(msgs), files)
			}) {
				return
			}

			// Single file

			files := t.getAllFileURLs(msg)
			body := msg.Text
			if body == "" {
				body = msg.Caption
			}
			replyMessage(decodeUidInvisible(uidCode[0]), body, files)
		}
		log.Printf("Reply message received: %s", msg.Text)
		return
	}

	// Group files

	if t.bufferAlbumMessage(msg, func(msgs []*tgbotapi.Message) {
		t.processAlbum(msgs)
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

func (t *TelegramBot) processAlbum(msgs []*tgbotapi.Message) {
	for _, msg := range msgs {
		files := t.getAllFileURLs(msg)
		// обрабатывай как надо
		log.Println("Album file(s):", files)
	}
}

// Decode Encode UID

var invisibleRunes = []rune{
	'\u200B', // 0 Zero Width Space
	'\u200C', // 1 Zero Width Non-Joiner
	'\u200D', // 2 Zero Width Joiner
	'\u2060', // 3 Word Joiner
	'\uFEFF', // 4 Zero Width No-Break Space
	'\u2061', // 5 Function Application
	'\u2062', // 6 Invisible Times
	'\u2063', // 7 Invisible Separator
	'\u2064', // 8 Invisible Plus
	'\u034F', // 9 Combining Grapheme Joiner
}

var runeToDigit = func() map[rune]int {
	m := make(map[rune]int)
	for i, r := range invisibleRunes {
		m[r] = int(i)
	}
	return m
}()

var invisibleSet = func() map[rune]bool {
	m := make(map[rune]bool)
	for _, r := range invisibleRunes {
		m[r] = true
	}
	return m
}()

func encodeUidInvisible(n int) string {
	if n == 0 {
		return string(invisibleRunes[0])
	}

	var result []rune
	for n > 0 {
		digit := n % 10
		result = append([]rune{invisibleRunes[digit]}, result...)
		n /= 10
	}
	return string(result)
}

func decodeUidInvisible(s string) int {
	var result int
	for _, r := range s {
		if d, ok := runeToDigit[r]; ok {
			result = result*10 + d
		}
	}
	return result
}

func findInvisibleUidSequences(s string) []string {
	var sequences []string
	var current []rune

	for _, r := range s {
		if invisibleSet[r] {
			current = append(current, r)
		} else if len(current) > 0 {
			sequences = append(sequences, string(current))
			current = nil
		}
	}
	if len(current) > 0 {
		sequences = append(sequences, string(current))
	}

	return sequences
}
