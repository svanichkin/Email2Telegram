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

	"context" // Added for Telego
	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
	// tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5" // Replaced by telego
	telehtml "github.com/svanichkin/TelegramHTML"
)

type TelegramBot struct {
	api         *telego.Bot
	recipientId int64
	token       string // Token might still be useful for file URLs if not directly provided by a telego helper
	updates     <-chan telego.Update
	isChat      bool
	topics      map[string]string
	// Add a context for bot operations, can be initialized in NewTelegramBot
	ctx context.Context
}

func NewTelegramBot(apiToken string, recipientID int64) (*TelegramBot, error) {
	// Optional: Add default logger for Telego
	// logger := telego.NewLogger(log.Default())
	// bot, err := telego.NewBot(apiToken, telego.WithLogger(logger))
	bot, err := telego.NewBot(apiToken, telego.WithDefaultDebugLogger())
	if err != nil {
		return nil, fmt.Errorf("failed to create Telegram bot with Telego: %w", err)
	}

	// Get bot info
	botUser, err := bot.GetMe(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get bot info (GetMe) with Telego: %w", err)
	}
	log.Printf("Authorized on account %s", botUser.Username)

	updates, err := bot.UpdatesViaLongPolling(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get updates channel from Telego: %w", err)
	}

	return &TelegramBot{
		api:         bot,
		recipientId: recipientID,
		token:       apiToken, // Storing token for potential use with file URLs
		updates:     updates,
		ctx:         context.Background(), // Initialize a background context
	}, nil
}

func (t *TelegramBot) StartListener(
	replayMessage func(uid int, message string, files []struct{ Url, Name string }),
	newMessage func(to string, title string, message string, files []struct{ Url, Name string }),
) {
	// Ensure context is not nil
	if t.ctx == nil {
		t.ctx = context.Background()
	}
	go func() {
		for update := range t.updates {
			// Create a new context for each update, or use t.ctx
			// For now, using t.ctx; consider per-update context if needed for timeouts/cancellation
			t.handleUpdate(update, replayMessage, newMessage)
		}
	}()
}

func (tb *TelegramBot) SendMessage(msg string) error {
	message := tu.Message(
		tu.ID(tb.recipientId),
		msg,
	)
	_, err := tb.api.SendMessage(tb.ctx, message)
	if err != nil {
		log.Printf("Error sending message with Telego: %v", err)
		return fmt.Errorf("failed to send message via Telego: %w", err)
	}
	return nil
}

func (tb *TelegramBot) RequestUserInput(prompt string) (string, error) {
	if tb.api == nil {
		return "", errors.New("telego API is not initialized")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background() // Ensure context is not nil
	}

	message := tu.Message(
		tu.ID(tb.recipientId),
		prompt,
	)
	if _, err := tb.api.SendMessage(tb.ctx, message); err != nil {
		return "", fmt.Errorf("failed to send prompt with Telego: %w", err)
	}
	log.Printf("Sent prompt to recipient ID %d: %s", tb.recipientId, prompt)

	// Ensure updates channel is active, though it should be from NewTelegramBot
	if tb.updates == nil {
		var err error
		tb.updates, err = tb.api.UpdatesViaLongPolling(tb.ctx, nil)
		if err != nil {
			return "", fmt.Errorf("failed to re-initialize updates channel in RequestUserInput: %w", err)
		}
	}

	timeout := time.After(5 * time.Minute) // TODO: Make timeout configurable or pass via context

	for {
		select {
		case update, ok := <-tb.updates:
			if !ok {
				log.Println("Updates channel closed unexpectedly in RequestUserInput")
				return "", errors.New("updates channel closed")
			}
			if update.Message == nil {
				continue
			}
			// Ensure Chat is not nil before accessing ID - REMOVED, as telego.Chat is a struct.
			// if update.Message.Chat == nil {
			// 	log.Printf("RequestUserInput: Ignored message with nil Chat object")
			// 	continue
			// }
			if update.Message.Chat.ID == tb.recipientId { // Chat will be present if Message is.
				log.Printf("Received reply: %s", update.Message.Text)
				return update.Message.Text, nil
			}
			// Ensure From is not nil before accessing ID
			fromID := int64(0)
			if update.Message.From != nil {
				fromID = update.Message.From.ID
			}
			log.Printf("RequestUserInput: Ignored message in chat %d from user %d (expected chat %d)", update.Message.Chat.ID, fromID, tb.recipientId)

		case <-timeout:
			log.Println("Timeout waiting for input reply.")
			return "", errors.New("timeout waiting for input reply")
		}
	}
}

func (tb *TelegramBot) SendEmailData(data *ParsedEmailData, isCode, isSpam bool) error {
	if tb.api == nil {
		return errors.New("telego API is not initialized in SendEmailData")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}
	if data == nil {
		return errors.New("parsed email data is nil")
	}

	var topicIDStr string // Renamed to avoid conflict with topicIdInt
	var topicIDInt int    // To store the integer value of topic ID for Telego methods

	if tb.isChat && !isSpam {
		if tb.topics == nil {
			s := fmt.Sprint(tb.recipientId)
			// Assuming LoadAndDecrypt returns map[string]string and error
			loadedTopics, errLoad := LoadAndDecrypt(s, s+".top")
			if errLoad != nil {
				log.Printf("Failed to load topics: %v. Starting with empty topics map.", errLoad)
				tb.topics = make(map[string]string)
			} else {
				tb.topics = loadedTopics
			}
			if tb.topics == nil { // Ensure topics is not nil after potential failed load
				tb.topics = make(map[string]string)
			}
		}
		cleanSubj := cleanSubject(data.Subject)
		topicIDStr = tb.topics[cleanSubj]
		if topicIDStr == "" {
			log.Printf("No existing topic found for subject: '%s'. Attempting to create one.", cleanSubj)
			var errCreate error
			topicIDInt, errCreate = tb.ensureTopic(cleanSubj)
			if errCreate != nil {
				return fmt.Errorf("topic handling error (ensureTopic failed): %w", errCreate)
			}
			topicIDStr = fmt.Sprint(topicIDInt)
			tb.topics[cleanSubj] = topicIDStr

			recipientIdStr := fmt.Sprint(tb.recipientId)
			// EncryptAndSave might need error handling too
			if errSave := EncryptAndSave(recipientIdStr, recipientIdStr+".top", tb.topics); errSave != nil {
				log.Printf("Failed to save updated topics: %v", errSave)
				// Continue, as the message can still be sent to the newly created topic
			}
			log.Printf("Created and saved new topic ID %s for subject: '%s'", topicIDStr, cleanSubj)
		} else {
			var errConv error
			topicIDInt, errConv = strconv.Atoi(topicIDStr)
			if errConv != nil {
				log.Printf("Error converting stored topicID '%s' to int: %v. Attempting to re-create topic.", topicIDStr, errConv)
				// Fallback: try to ensure/create topic again if conversion fails
				var errReCreate error
				topicIDInt, errReCreate = tb.ensureTopic(cleanSubj)
				if errReCreate != nil {
					return fmt.Errorf("topic handling error (re-create failed for %s): %w", cleanSubj, errReCreate)
				}
				topicIDStr = fmt.Sprint(topicIDInt)
				tb.topics[cleanSubj] = topicIDStr // Update with the new (or re-confirmed) ID
				recipientIdStr := fmt.Sprint(tb.recipientId)
				if errSave := EncryptAndSave(recipientIdStr, recipientIdStr+".top", tb.topics); errSave != nil {
					log.Printf("Failed to save updated topics after re-creation: %v", errSave)
				}
			}
		}
	}

	// if code message
	if isCode {
		if !tb.isChat || topicIDInt == 0 { // Send as direct message if not a chat or topicId is invalid
			titleText := "🔑 <b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>"
			fullMessage := titleText + telehtml.EncodeIntInvisible(data.Uid)

			msgParams1 := tu.Message(tu.ID(tb.recipientId), fullMessage)
			msgParams1.ParseMode = telego.ModeHTML
			disablePreview1 := true
			msgParams1.DisableWebPagePreview = &disablePreview1

			if _, err := tb.api.SendMessage(tb.ctx, msgParams1); err != nil {
				return fmt.Errorf("failed to send title message (code) with Telego: %w", err)
			}

			// Send the code body as a separate message
			bodyMsgParams := tu.Message(tu.ID(tb.recipientId), data.TextBody) // Code is usually plain text
			if _, err := tb.api.SendMessage(tb.ctx, bodyMsgParams); err != nil {
				return fmt.Errorf("failed to send code message body with Telego: %w", err)
			}
		} else { // Sending to a topic
			msgParams2 := tu.Message(tu.ID(tb.recipientId), data.TextBody)
			msgParams2.ParseMode = telego.ModeHTML
			disablePreview2 := true
			msgParams2.DisableWebPagePreview = &disablePreview2
			msgParams2.MessageThreadID = topicIDInt

			if _, err := tb.api.SendMessage(tb.ctx, msgParams2); err != nil {
				return fmt.Errorf("failed to send code message to topic %d with Telego: %w", topicIDInt, err)
			}
		}
		return nil
	}

	// Header + text, then split
	var fullTextBody string
	if !tb.isChat || topicIDInt == 0 { // Not a chat, or topic ID is invalid, send full header + body
		header := "<b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>" + "\n\n"
		if isSpam {
			header = "🚫 " + header
		} else {
			header = "✉️ " + header
		}
		fullTextBody = header + data.TextBody
	} else { // Is a chat with a valid topic, send only the body to the topic (header is in topic title)
		fullTextBody = data.TextBody
	}

	textToSend := fullTextBody
	if !tb.isChat || topicIDInt == 0 {
		textToSend = fullTextBody + telehtml.EncodeIntInvisible(data.Uid)
	}

	messages := telehtml.SplitTelegramHTML(textToSend)

	// Send messages
	for _, messagePart := range messages {
		msgParams := tu.Message(tu.ID(tb.recipientId), messagePart)
		msgParams.ParseMode = telego.ModeHTML
		disablePreview := true
		msgParams.DisableWebPagePreview = &disablePreview

		if tb.isChat && topicIDInt != 0 { // Message to topic
			msgParams.MessageThreadID = topicIDInt
		}
		// Note: if !tb.isChat || topicIDInt == 0, MessageThreadID remains 0/nil, which is correct for direct messages.

		if _, err := tb.api.SendMessage(tb.ctx, msgParams); err != nil {
			target := "direct message"
			if tb.isChat && topicIDInt != 0 {
				target = fmt.Sprintf("message to topic %d", topicIDInt)
			}
			return fmt.Errorf("failed to send main %s part with Telego: %w", target, err)
		}
	}

	// Other Attachments
	if len(data.Attachments) > 0 {
		log.Printf("Attempting to send %d attachments for email UID %d", len(data.Attachments), data.Uid)
		for filename, contentBytes := range data.Attachments {
			if len(contentBytes) == 0 {
				log.Printf("Skipping attachment '%s' for email UID %d due to empty content.", filename, data.Uid)
				continue
			}

			inputFile := tu.FileFromReader(filename, bytes.NewReader(contentBytes))

			var docParams *telego.SendDocumentParams

			if !tb.isChat || topicIDInt == 0 { // Send as a direct document
				docParams = tu.Document(tu.ID(tb.recipientId), inputFile)
				// Optionally add caption, parse_mode for caption, etc.
				// For simplicity, matching original behavior of sending document without extra text.
			} else { // Send document to a specific topic
				docParams = tu.Document(tu.ID(tb.recipientId), inputFile).
					WithMessageThreadID(topicIDInt)
				// Again, caption can be added here if needed.
			}

			// Add a general caption if desired, e.g., indicating it's an attachment.
			// The original code did not add captions to attachments sent this way.
			// Example: docParams = docParams.WithCaption("Attachment: " + filename)

			log.Printf("Sending attachment '%s' (size: %d bytes) for email UID %d. Target: chat %d, topic %d (0 if direct)",
				filename, len(contentBytes), data.Uid, tb.recipientId, topicIDInt)

			if _, err := tb.api.SendDocument(tb.ctx, docParams); err != nil {
				target := "direct message"
				if tb.isChat && topicIDInt != 0 {
					target = fmt.Sprintf("topic %d", topicIDInt)
				}
				return fmt.Errorf("failed to send attachment %s to %s (email UID %d) with Telego: %w", filename, target, data.Uid, err)
			}
			log.Printf("Successfully sent attachment '%s' for email UID %d to chat %d, topic %d", filename, data.Uid, tb.recipientId, topicIDInt)
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
	if tb.api == nil {
		return 0, errors.New("telego API not initialized in ensureTopic")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}

	// Telego provides a direct method for creating forum topics.
	// The icon_color needs to be an int32. The original code used a string "1".
	// Valid colors: 7322096 (0x6FB9F0), 16766590 (0xFFD67E), 13338331 (0xCB86DB),
	// 9367192 (0x8EEE98), 16749490 (0xFF93B2), or 16478047 (0xFB6F5F)
	// Let's pick one, e.g., 0x6FB9F0
	iconColorValue := int32(0x6FB9F0) // Blue
	// TODO: Consider making icon color random from the list as the comment suggested.

	params := &telego.CreateForumTopicParams{
		ChatID:    tu.ID(tb.recipientId),
		Name:      subject,
		IconColor: &iconColorValue, // Reverted to pointer, as per type definition.
	}

	forumTopic, err := tb.api.CreateForumTopic(tb.ctx, params)
	if err != nil {
		return 0, fmt.Errorf("telego CreateForumTopic error: %w", err)
	}

	// The created ForumTopic object directly contains the MessageThreadID
	if forumTopic == nil {
		return 0, errors.New("telego CreateForumTopic returned nil topic")
	}

	return forumTopic.MessageThreadID, nil
}

func (tb *TelegramBot) CheckAndRequestAdminRights(chatID int64) (bool, error) {
	if tb.api == nil {
		return false, errors.New("telego API is not initialized in CheckAndRequestAdminRights")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}

	// Get bot's own ID
	botUser, err := tb.api.GetMe(tb.ctx) // Assuming GetMe is efficient or cached by library/user
	if err != nil {
		return false, fmt.Errorf("failed to get bot info (GetMe) in CheckAndRequestAdminRights: %w", err)
	}
	botID := botUser.ID

	// Get chat member information for the bot in the specified chat
	chatMember, err := tb.api.GetChatMember(tb.ctx, &telego.GetChatMemberParams{
		ChatID: tu.ID(chatID),
		UserID: botID,
	})
	if err != nil {
		return false, fmt.Errorf("telego failed to get chat member info for bot %d in chat %d: %w", botID, chatID, err)
	}

	// Check if the bot is an administrator or creator
	status := chatMember.ChatMemberKind() // Corrected: Use ChatMemberKind() method
	log.Printf("Bot status in chat %d is: %s", chatID, status)

	// Valid statuses for admin-like rights are "creator" or "administrator"
	// telego constants for these are telego.ChatMemberStatusCreator and telego.ChatMemberStatusAdministrator
	if status != telego.ChatMemberStatusCreator && status != telego.ChatMemberStatusAdministrator {
		// The original code returned an error here to signal main.go to send a message.
		// We should keep this behavior or refactor message sending into this function.
		// For now, returning error to match original logic flow.
		// The message itself is sent from main.go after this check.
		// Let's return a specific error or a boolean that indicates rights are missing.
		// The original function returned `fmt.Errorf("sent admin rights request to chat %d", chatID)`
		// which was a bit misleading. Let's return a clear boolean and let main handle the message.
		// For consistency with the original, we'll return (false, nil) if rights are missing and message should be sent.
		// However, the original error `fmt.Errorf("sent admin rights request to chat %d", chatID)` was used by main.go to
		// decide to send a message. This seems like a misuse of error.
		// Let's change to return (false, nil) if rights are missing, and main.go should check the boolean.
		// The original code in main.go:
		// if err != nil { log.Printf("Error during CheckAndRequestAdminRights: %v", err) }
		// else if !adminEnabled { /* send message */ }
		// This implies that an error means something went wrong with the check itself,
		// and !adminEnabled (with no error) means the check was successful and rights are missing.
		// So, if status is not admin/creator, we return (false, nil).
		log.Printf("Bot does not have admin rights in chat %d. Status: %s", chatID, status)
		return false, nil // Signal that rights are missing, but no error occurred during the check itself.
	}

	log.Printf("Bot already has admin rights in chat %d (status: %s)", chatID, status)
	return true, nil
}

func (tb *TelegramBot) CheckTopicsEnabled(chatID int64) (bool, error) {
	tb.isChat = false // Default to false

	if tb.api == nil {
		return tb.isChat, errors.New("telego API not initialized in CheckTopicsEnabled")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}

	// The original code made a raw request: tb.api.MakeRequest("getForumTopicIconStickers", params)
	// The actual API method `getForumTopicIconStickers` doesn't take a chat_id.
	// It was likely a misinterpretation or an attempt to check if the chat is a forum by calling a forum-related method.
	// A more reliable way to check if a supergroup chat has topics enabled is to fetch the chat details.
	// The erroneous GetForumTopicIconStickers call has been removed from here.
	// It seems the original code was misusing this or relying on a side effect.
	// A more reliable way to check if a supergroup chat has topics enabled is to fetch the chat details.
	chat, errChat := tb.api.GetChat(tb.ctx, &telego.GetChatParams{ChatID: tu.ID(chatID)})
	if errChat != nil {
		// If we can't get chat details, we can't determine if topics are enabled.
		return tb.isChat, fmt.Errorf("failed to get chat details for %d: %w", chatID, errChat)
	}

	// According to Telegram Bot API, a forum is a supergroup with topics enabled.
	// The `Chat` object has an `IsForum` field (optional).
	if chat.IsForum != nil && *chat.IsForum {
		tb.isChat = true // Topics are enabled
		log.Printf("Topics are enabled for chat ID %d.", chatID)
		return tb.isChat, nil
	}

	// If IsForum is false or nil, topics are not enabled, or it's not a forum-capable chat.
	log.Printf("Topics are NOT enabled for chat ID %d (IsForum: %v). The chat might not be a forum or topics are disabled.", chatID, chat.IsForum)
	// The original code checked for "the chat is not a forum" in the error message.
	// If GetChat works but IsForum is false/nil, it means it's not a forum with topics.
	return tb.isChat, nil // No error, but topics are not enabled.
}

// Events from user

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

func (t *TelegramBot) handleUpdate(
	update telego.Update,
	replayMessageFunc func(uid int, message string, files []struct{ Url, Name string }), // Renamed for clarity
	newMessageFunc func(to string, title string, message string, files []struct{ Url, Name string }), // Renamed for clarity
) {
	if t.ctx == nil {
		t.ctx = context.Background() // Ensure context
	}

	if update.Message == nil {
		return
	}
	msg := update.Message // msg is now *telego.Message

	// The primary check: is the message from the configured chat/user?
	if msg.Chat.ID != t.recipientId {
		fromID := int64(0)
		if msg.From != nil {
			fromID = msg.From.ID
		}
		senderChatID := int64(0)
		// Note: telego.Message doesn't have SenderChat directly.
		// If this is needed, it might be part of a different update type or field.
		// For now, assuming direct user messages or bot is added to group.
		// if msg.SenderChat != nil { senderChatID = msg.SenderChat.ID }
		log.Printf("Ignoring message from unexpected chat: MessageChat.ID=%d, From.ID=%d. Expected recipientID: %d", msg.Chat.ID, fromID, t.recipientId)
		return
	}

	// Logging fromID safely
	fromIDLog := int64(0)
	if msg.From != nil {
		fromIDLog = msg.From.ID
	}
	log.Printf("Processing message from Chat.ID %d (RecipientID: %d), From.ID %d", msg.Chat.ID, t.recipientId, fromIDLog)

	// Reply message
	if msg.ReplyToMessage != nil {
		// msg.ReplyToMessage is also *telego.Message
		repliedText := msg.ReplyToMessage.Text // Text of the message being replied to
		// Caption might also be relevant for media
		if repliedText == "" && msg.ReplyToMessage.Caption != "" {
			repliedText = msg.ReplyToMessage.Caption
		}

		if uidCode := telehtml.FindInvisibleIntSequences(repliedText); len(uidCode) > 0 {
			uidToReply := telehtml.DecodeIntInvisible(uidCode[0])

			// Group files (album)
			if msg.MediaGroupID != "" { // Check MediaGroupID on the current message 'msg'
				if t.bufferAlbumMessage(msg, func(albumMsgs []*telego.Message) {
					files := []struct{ Url, Name string }{}
					for _, m := range albumMsgs {
						files = append(files, t.getAllFileURLs(m)...)
					}
					replayMessageFunc(uidToReply, extractTextFromMessages(albumMsgs), files)
				}) {
					return // Buffered, will be processed by callback
				}
			}

			// Single file / non-album message
			files := t.getAllFileURLs(msg)
			body := msg.Text
			if body == "" {
				body = msg.Caption
			}
			replayMessageFunc(uidToReply, body, files)
			return
		}
		// If ReplyToMessage is not nil, but no UID code found, it's a generic reply not tied to an email.
		// Original code implies such replies are ignored or fall through. For now, let's let it fall through
		// to be treated as a potential new message, though this might be confusing.
		// A more explicit handling might be to inform the user if they reply to a non-email message.
		// For now, matching original behavior of returning if UID is processed.
		// If no UID, it means this reply wasn't to one of *our* specially encoded messages.
		// The original code just `return`s after the `if msg.ReplyToMessage != nil` block if a UID was processed.
		// If no UID, it would then fall through to "New Message" logic. This seems like a potential bug in original.
		// Let's assume for now that if it's a reply but not to a UID-encoded message, we treat it as a new message attempt.
		// This matches the fall-through.
	}

	// New Message or unhandled Reply
	// Group files (album) for new message
	if msg.MediaGroupID != "" {
		if t.bufferAlbumMessage(msg, func(albumMsgs []*telego.Message) {
			var rawText string
			for _, m := range albumMsgs { // m is *telego.Message
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
				log.Println("Invalid mail format in album message for new email.")
				// Send help message for albums? Original only sends for single messages.
				// For now, just log.
				return
			}
			files := []struct{ Url, Name string }{}
			for _, m := range albumMsgs {
				files = append(files, t.getAllFileURLs(m)...)
			}
			newMessageFunc(to, title, body, files)
		}) {
			return // Buffered, will be processed by callback
		}
	}

	// Single file / non-album new message
	msgText := msg.Text
	if msgText == "" {
		msgText = msg.Caption
	}

	to, title, body, ok := parseMailContent(msgText)
	if !ok {
		log.Println("Invalid mail format in single message for new email.")
		// Send help message
		_ = t.SendMessage("Hi! I'm your mail bot.") // Errors ignored in original, match for now
		_ = t.SendMessage("To reply to an email, just reply to the message and \n\nenter your text, and attach files if needed.")
		_ = t.SendMessage("To send a new email, use the format:\n\nto.user@mail.example.com\nSubject line\nEmail text\n\nAttach files if needed.")
		return
	}
	files := t.getAllFileURLs(msg)
	newMessageFunc(to, title, body, files)
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
