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

	// Construct the full URL. Telego Bot object has a method for this.
	return t.api.FileDownloadURL(file.FilePath), nil
}

func (t *TelegramBot) getAllFileURLs(msg *telego.Message) []struct{ Url, Name string } {
	files := []struct{ Url, Name string }{}
	var url string
	var err error

	if msg.Document != nil {
		url, err = t.getFileURL(msg.Document.FileID)
		if err == nil && url != "" {
			files = append(files, struct{ Url, Name string }{url, msg.Document.FileName})
		} else if err != nil {
			log.Printf("Error getting file URL for document %s: %v", msg.Document.FileID, err)
		}
	}
	if msg.Audio != nil {
		url, err = t.getFileURL(msg.Audio.FileID)
		if err == nil && url != "" {
			fileName := "audio.mp3"
			if msg.Audio.FileName != "" {
				fileName = msg.Audio.FileName
			}
			files = append(files, struct{ Url, Name string }{url, fileName})
		} else if err != nil {
			log.Printf("Error getting file URL for audio %s: %v", msg.Audio.FileID, err)
		}
	}
	if msg.Video != nil {
		url, err = t.getFileURL(msg.Video.FileID)
		if err == nil && url != "" {
			fileName := "video.mp4"
			if msg.Video.FileName != "" {
				fileName = msg.Video.FileName
			}
			files = append(files, struct{ Url, Name string }{url, fileName})
		} else if err != nil {
			log.Printf("Error getting file URL for video %s: %v", msg.Video.FileID, err)
		}
	}
	if msg.Voice != nil {
		url, err = t.getFileURL(msg.Voice.FileID)
		if err == nil && url != "" {
			files = append(files, struct{ Url, Name string }{url, "voice.ogg"}) // Voice usually doesn't have a filename
		} else if err != nil {
			log.Printf("Error getting file URL for voice %s: %v", msg.Voice.FileID, err)
		}
	}
	if msg.Animation != nil {
		url, err = t.getFileURL(msg.Animation.FileID)
		if err == nil && url != "" {
			fileName := "animation.mp4"
			if msg.Animation.FileName != "" {
				fileName = msg.Animation.FileName
			}
			files = append(files, struct{ Url, Name string }{url, fileName})
		} else if err != nil {
			log.Printf("Error getting file URL for animation %s: %v", msg.Animation.FileID, err)
		}
	}
	if msg.VideoNote != nil {
		url, err = t.getFileURL(msg.VideoNote.FileID)
		if err == nil && url != "" {
			files = append(files, struct{ Url, Name string }{url, "video_note.mp4"})
		} else if err != nil {
			log.Printf("Error getting file URL for video note %s: %v", msg.VideoNote.FileID, err)
		}
	}
	if len(msg.Photo) > 0 {
		photo := msg.Photo[len(msg.Photo)-1] // Get the largest photo
		url, err = t.getFileURL(photo.FileID)
		if err == nil && url != "" {
			files = append(files, struct{ Url, Name string }{url, "photo.jpg"}) // Photos don't have explicit server-side filenames
		} else if err != nil {
			log.Printf("Error getting file URL for photo %s: %v", photo.FileID, err)
		}
	}

	return files
}

func extractTextFromMessages(msgs []*telego.Message) string { // Type changed
	for _, m := range msgs {
		if m.Text != "" {
			return m.Text
		}
		if m.Caption != "" { // telego.Message has Caption string
			return m.Caption
		}
	}
	return ""
}

func parseMailContent(msgText string) (to, title, body string, ok bool) {
	// This function only does string manipulation, no changes needed due to Telego.
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

func (t *TelegramBot) bufferAlbumMessage(msg *telego.Message, callback func([]*telego.Message)) bool { // Types changed
	if msg.MediaGroupID == "" {
		return false // Not part of an album
	}

	albumLock.Lock()
	defer albumLock.Unlock()

	entry, exists := albumBuffer[msg.MediaGroupID]
	if !exists {
		entry = &albumEntry{} // albumEntry messages type will need to be *telego.Message
		albumBuffer[msg.MediaGroupID] = entry
	}

	entry.messages = append(entry.messages, msg)

	// If a timer for this album already exists, stop it.
	if entry.timer != nil {
		entry.timer.Stop()
	}

	// Start a new timer. If more messages for this album arrive within the timeout,
	// this timer will be stopped and reset.
	entry.timer = time.AfterFunc(1*time.Second, func() { // Duration can be configured
		albumLock.Lock()
		defer albumLock.Unlock()

		// Ensure the entry still exists, as it might have been processed and deleted by a rapid succession of events.
		// This check is mostly for safety, as the timer should be the one to delete it.
		currentEntry, stillExists := albumBuffer[msg.MediaGroupID]
		if !stillExists || currentEntry != entry { // If entry changed or deleted, this timer is stale
			return
		}

		// Process the buffered messages
		callback(currentEntry.messages)

		// Remove the album from the buffer after processing
		delete(albumBuffer, msg.MediaGroupID)
	})

	return true // Message was buffered
}

type albumEntry struct {
	messages []*telego.Message // Type changed
	timer    *time.Timer
}

var albumBuffer = make(map[string]*albumEntry) // Key: MediaGroupID, Value: albumEntry
var albumLock sync.Mutex
