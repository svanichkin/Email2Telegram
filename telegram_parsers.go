package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mymmrac/telego"
)

func cleanSubject(subject string) string {

	re := regexp.MustCompile(`(?i)^(re|fwd|fw):\s*`)
	for re.MatchString(subject) {
		subject = re.ReplaceAllString(subject, "")
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ""
	}
	runes := []rune(strings.ToLower(subject))
	runes[0] = unicode.ToUpper(runes[0])

	return string(runes)

}

type FileAttachment struct {
	Name string
	Mime string
	Data []byte
}

func (t *TelegramBot) getFileURL(fileID string) (string, error) {

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Cyan("Getting file URL for ID: %s").String(), fileID)
	if t.api == nil {
		return "", errors.New("telego API not initialized in getFileURL")
	}
	if t.ctx == nil {
		t.ctx = context.Background()
	}

	file, err := t.api.GetFile(t.ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return "", fmt.Errorf("telego failed to get file: %w", err)
	}
	if file.FilePath == "" {
		return "", fmt.Errorf("telego GetFile returned empty file_path for FileID: %s", fileID)
	}

	url := t.api.FileDownloadURL(file.FilePath)
	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Green("Got file URL: %s").String(), url)

	return url, nil

}

func (t *TelegramBot) getAllFileURLs(msg *telego.Message) []struct{ Url, Name string } {

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Cyan("Processing attachments...").String())
	files := []struct{ Url, Name string }{}
	var url string
	var err error

	processAttachment := func(fileType, fileID, fileName string) {
		url, err = t.getFileURL(fileID)
		if err != nil {
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Red("Error getting %s file URL %s: %v").String(),
				fileType, fileID, err)
			return
		}
		if url == "" {
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Empty URL for %s file %s").String(),
				fileType, fileID)
			return
		}
		files = append(files, struct{ Url, Name string }{url, fileName})
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Green("Added %s: %s").String(), fileType, fileName)
	}

	if msg.Document != nil {
		processAttachment("document", msg.Document.FileID, msg.Document.FileName)
	}
	if msg.Audio != nil {
		fileName := "audio.mp3"
		if msg.Audio.FileName != "" {
			fileName = msg.Audio.FileName
		}
		processAttachment("audio", msg.Audio.FileID, fileName)
	}
	if msg.Video != nil {
		fileName := "video.mp4"
		if msg.Video.FileName != "" {
			fileName = msg.Video.FileName
		}
		processAttachment("video", msg.Video.FileID, fileName)
	}
	if msg.Voice != nil {
		processAttachment("voice", msg.Voice.FileID, "voice.ogg")
	}
	if msg.Animation != nil {
		fileName := "animation.mp4"
		if msg.Animation.FileName != "" {
			fileName = msg.Animation.FileName
		}
		processAttachment("animation", msg.Animation.FileID, fileName)
	}
	if msg.VideoNote != nil {
		processAttachment("video note", msg.VideoNote.FileID, "video_note.mp4")
	}
	if len(msg.Photo) > 0 {
		photo := msg.Photo[len(msg.Photo)-1]
		processAttachment("photo", photo.FileID, "photo.jpg")
	}
	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Green("Found %d attachments").String(), len(files))

	return files

}

func extractTextFromMessages(msgs []*telego.Message) string {

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

func (t *TelegramBot) bufferAlbumMessage(msg *telego.Message, callback func([]*telego.Message)) bool {

	if msg.MediaGroupID == "" {
		return false
	}

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Magenta("Buffering album message with ID: %s").String(), msg.MediaGroupID)
	albumLock.Lock()
	defer albumLock.Unlock()

	entry, exists := albumBuffer[msg.MediaGroupID]
	if !exists {
		entry = &albumEntry{}
		albumBuffer[msg.MediaGroupID] = entry
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Created new album entry for ID: %s").String(), msg.MediaGroupID)
	}

	entry.messages = append(entry.messages, msg)
	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Added message to album, total: %d").String(), len(entry.messages))

	if entry.timer != nil {
		entry.timer.Stop()
		log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Yellow("Stopped previous timer").String())
	}

	entry.timer = time.AfterFunc(1*time.Second, func() {
		albumLock.Lock()
		defer albumLock.Unlock()

		currentEntry, stillExists := albumBuffer[msg.MediaGroupID]
		if !stillExists || currentEntry != entry {
			log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Yellow("Stale timer, album entry changed or deleted").String())
			return
		}

		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Green("Processing album with %d messages").String(), len(currentEntry.messages))
		callback(currentEntry.messages)
		delete(albumBuffer, msg.MediaGroupID)
	})

	return true

}

type albumEntry struct {
	messages []*telego.Message
	timer    *time.Timer
}

var albumBuffer = make(map[string]*albumEntry)
var albumLock sync.Mutex
