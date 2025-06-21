package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"strconv"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
	"github.com/logrusorgru/aurora/v4"
	telehtml "github.com/svanichkin/TelegramHTML"
)

// Re-declare 'au' here similar to email_client.go for consistency
// This assumes 'au' is initialized in main.go.
var au aurora.Aurora

func init() {
	if au == nil {
		au = aurora.New(aurora.WithColors(true))
	}
}

type TelegramBot struct {
	api         *telego.Bot
	recipientId int64
	token       string
	updates     <-chan telego.Update
	isChat      bool
	topics      map[string]string
	ctx         context.Context
}

func NewTelegramBot(apiToken string, recipientID int64) (*TelegramBot, error) {

	bot, err := telego.NewBot(apiToken, telego.WithDiscardLogger())
	if err != nil {
		return nil, fmt.Errorf("failed to create Telegram bot with Telego: %w", err)
	}

	// Get bot info

	log.Println(au.Gray(11, "[TELEGO]"), au.Yellow("Getting bot info (GetMe)..."))
	botUser, err := bot.GetMe(context.Background())
	if err != nil {
		log.Println(au.Gray(11, "[TELEGO]"), au.Red(aurora.Bold("Failed to get bot info (GetMe):")), au.Red(err))
		return nil, fmt.Errorf("failed to get bot info (GetMe) with Telego: %w", err)
	}
	log.Println(au.Gray(11, "[TELEGO]"), au.Green(fmt.Sprintf("Authorized on account %s (ID: %d)", botUser.Username, botUser.ID)))

	log.Println(au.Gray(11, "[TELEGO]"), au.Yellow("Setting up updates via long polling..."))
	updates, err := bot.UpdatesViaLongPolling(context.Background(), nil)
	if err != nil {
		log.Println(au.Gray(11, "[TELEGO]"), au.Red(aurora.Bold("Failed to get updates channel:")), au.Red(err))
		return nil, fmt.Errorf("failed to get updates channel from Telego: %w", err)
	}

	return &TelegramBot{
		api:         bot,
		recipientId: recipientID,
		token:       apiToken,
		updates:     updates,
		ctx:         context.Background(),
	}, nil

}

func (t *TelegramBot) StartListener(replayMessage func(uid int, message string, files []struct{ Url, Name string }), newMessage func(to string, title string, message string, files []struct{ Url, Name string })) {

	if t.ctx == nil {
		t.ctx = context.Background()
	}
	go func() {
		for update := range t.updates {
			t.handleUpdate(update, replayMessage, newMessage)
		}
	}()

}

func (tb *TelegramBot) SendMessage(msg string) error {

	message := tu.Message(
		tu.ID(tb.recipientId),
		msg,
	)
	log.Println(au.Gray(11, "[TELEGO_SEND]"), au.Yellow(fmt.Sprintf("Sending message to recipient %d: \"%s...\"", tb.recipientId, truncateMessage(msg, 50))))
	_, err := tb.api.SendMessage(tb.ctx, message)
	if err != nil {
		log.Println(au.Gray(11, "[TELEGO_SEND]"), au.Red("Error sending message with Telego:"), au.Red(err))
		return fmt.Errorf("failed to send message via Telego: %w", err)
	}
	log.Println(au.Gray(11, "[TELEGO_SEND]"), au.Green(fmt.Sprintf("Successfully sent message to recipient %d.", tb.recipientId)))
	return nil

}

func (tb *TelegramBot) RequestUserInput(prompt string) (string, error) {

	if tb.api == nil {
		return "", errors.New("telego API is not initialized")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}
	message := tu.Message(
		tu.ID(tb.recipientId),
		prompt,
	)
	if _, err := tb.api.SendMessage(tb.ctx, message); err != nil {
		log.Println(au.Gray(11, "[TELEGO_INPUT]"), au.Red("Failed to send prompt with Telego:"), au.Red(err))
		return "", fmt.Errorf("failed to send prompt with Telego: %w", err)
	}
	log.Println(au.Gray(11, "[TELEGO_INPUT]"), au.Cyan(fmt.Sprintf("Sent prompt to recipient ID %d: \"%s\"", tb.recipientId, prompt)))

	if tb.updates == nil {
		log.Println(au.Gray(11, "[TELEGO_INPUT]"), au.Yellow("Updates channel is nil, re-initializing..."))
		var err error
		tb.updates, err = tb.api.UpdatesViaLongPolling(tb.ctx, nil)
		if err != nil {
			log.Println(au.Gray(11, "[TELEGO_INPUT]"), au.Red("Failed to re-initialize updates channel:"), au.Red(err))
			return "", fmt.Errorf("failed to re-initialize updates channel in RequestUserInput: %w", err)
		}
		log.Println(au.Gray(11, "[TELEGO_INPUT]"), au.Green("Updates channel re-initialized."))
	}

	log.Println(au.Gray(11, "[TELEGO_INPUT]"), au.Yellow(fmt.Sprintf("Waiting for input (timeout: %s)...", (5 * time.Minute).String())))
	timeout := time.After(5 * time.Minute)
	for {
		select {
		case update, ok := <-tb.updates:
			if !ok {
				log.Println(au.Gray(11, "[TELEGO_INPUT]"), au.Red("Updates channel closed unexpectedly."))
				return "", errors.New("updates channel closed")
			}
			if update.Message == nil {
				// log.Println(au.Gray(11, "[TELEGO_INPUT]"), au.BrightBlack("Nil message in update, skipping."))
				continue
			}
			if update.Message.Chat.ID == tb.recipientId {
				log.Println(au.Gray(11, "[TELEGO_INPUT]"), au.Green(fmt.Sprintf("Received reply from %d: \"%s\"", update.Message.Chat.ID, update.Message.Text)))
				return update.Message.Text, nil
			}
			fromID := int64(0)
			if update.Message.From != nil {
				fromID = update.Message.From.ID
			}
			log.Println(au.Gray(11, "[TELEGO_INPUT]"), au.Yellow(fmt.Sprintf("Ignored message in chat %d from user %d (expected chat %d)", update.Message.Chat.ID, fromID, tb.recipientId)))

		case <-timeout:
			log.Println(au.Gray(11, "[TELEGO_INPUT]"), au.Red("Timeout waiting for input reply."))
			return "", errors.New("timeout waiting for input reply")
		}
	}
}

func (tb *TelegramBot) SendEmailData(data *ParsedEmailData, isCode, isSpam bool) error {
	if data == nil {
		log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Red("ParsedEmailData is nil, cannot send."))
		return errors.New("parsed email data is nil")
	}
	log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Yellow(fmt.Sprintf("Preparing to send email data for UID %d (Subject: %s). Spam: %t, Code: %t", data.Uid, data.Subject, isSpam, isCode)))

	if tb.api == nil {
		log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Red("Telego API not initialized."))
		return errors.New("telego API is not initialized in SendEmailData")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}
	if data == nil {
		return errors.New("parsed email data is nil")
	}
	var tid string
	var err error
	if tb.isChat && !isSpam {
		rid := fmt.Sprint(tb.recipientId)

		// If topics is nil, try loading or create empty

		log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Yellow("Chat mode active, handling topics..."))
		rid := fmt.Sprint(tb.recipientId)

		if tb.topics == nil {
			log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Yellow("Topics map is nil, attempting to load from file..."))
			tb.topics, err = LoadAndDecrypt(rid, rid+".top")
			if err != nil {
				log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Yellow(fmt.Sprintf("Failed to load topics: %v. Starting with empty map.", err)))
				tb.topics = make(map[string]string)
			} else {
				log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Green("Topics loaded successfully."))
			}
		}

		subj := cleanSubject(data.Subject)
		tid = tb.topics[subj]
		if tid == "" {
			log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Yellow(fmt.Sprintf("No existing topic for subject '%s'. Ensuring topic...", subj)))
			tid, err = tb.ensureTopic(subj) // ensureTopic logs its own steps
			if err != nil {
				log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Red("Failed to ensure topic:"), au.Red(err))
				return fmt.Errorf("topic handling error (ensureTopic failed): %w", err)
			}
			tb.topics[subj] = tid
			log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Yellow(fmt.Sprintf("Saving updated topics map with new topic ID %s for subject '%s'...", tid, subj)))
			if errSave := EncryptAndSave(rid, rid+".top", tb.topics); errSave != nil {
				log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Red("Failed to save updated topics:"), au.Red(errSave))
				// Continue, as the message might still be sendable
			} else {
				log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Green("Updated topics map saved."))
			}
		} else {
			log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Green(fmt.Sprintf("Found existing topic ID %s for subject '%s'.", tid, subj)))
		}
	}

	// if code message
	if isCode {
		log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Cyan(fmt.Sprintf("Handling as CODE message for UID %d.", data.Uid)))
		var codeMsgBody string
		if !tb.isChat { // For direct messages, include more context
			codeMsgBody = "🔑 <b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>" + telehtml.EncodeIntInvisible(data.Uid) + "\n\n" + data.TextBody
		} else { // For topics, just the code might be enough if context is in topic title
			codeMsgBody = data.TextBody + telehtml.EncodeIntInvisible(data.Uid) // Ensure UID is still there
		}

		if err := tb.sendMessage(tid, codeMsgBody); err != nil { // sendMessage logs its own errors
			log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Red(fmt.Sprintf("Failed to send code message for UID %d (topic: %s)", data.Uid, tid)), au.Red(err))
			return fmt.Errorf("failed to send code message (UID %d) with Telego: %w", data.Uid, err)
		}
		log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Green(fmt.Sprintf("Successfully sent CODE message for UID %d.", data.Uid)))
		return nil
	}

	// Regular message handling
	log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Cyan(fmt.Sprintf("Handling as REGULAR email message for UID %d.", data.Uid)))
	body := data.TextBody
	if !tb.isChat {
		header := "<b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>" + "\n\n"
		if isSpam {
			header = "🚫 " + header
			log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Magenta("Marked as SPAM."), au.Sprintf("UID %d", data.Uid))
		} else {
			header = "✉️ " + header
		}
		body = header + body
	}
	messages := telehtml.SplitTelegramHTML(body + telehtml.EncodeIntInvisible(data.Uid))
	log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Yellow(fmt.Sprintf("Split message for UID %d into %d parts.", data.Uid, len(messages))))

	for i, msgPart := range messages {
		if len(msgPart) > 0 {
			log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Yellow(fmt.Sprintf("Sending part %d/%d for UID %d (length: %d)...", i+1, len(messages), data.Uid, len(msgPart))))
			if err := tb.sendMessage(tid, msgPart); err != nil { // sendMessage logs its own errors
				log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Red(fmt.Sprintf("Failed to send message part %d/%d for UID %d:", i+1, len(messages), data.Uid)), au.Red(err))
				return fmt.Errorf("failed to send message part %d for UID %d: %w", i+1, data.Uid, err)
			}
		}
	}
	log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Green(fmt.Sprintf("All parts for UID %d sent successfully.", data.Uid)))

	// Send attachments
	if len(data.Attachments) > 0 {
		log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Yellow(fmt.Sprintf("Attempting to send %d attachments for email UID %d.", len(data.Attachments), data.Uid)))
		for filename, contentBytes := range data.Attachments {
			if len(contentBytes) == 0 {
				log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Yellow(fmt.Sprintf("Skipping attachment '%s' for UID %d due to empty content.", filename, data.Uid)))
				continue
			}
			log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Yellow(fmt.Sprintf("Sending attachment '%s' (%d bytes) for UID %d...", filename, len(contentBytes), data.Uid)))
			inputFile := tu.FileFromReader(bytes.NewReader(contentBytes), filename)
			var docParams *telego.SendDocumentParams
			if !tb.isChat {
				docParams = tu.Document(tu.ID(tb.recipientId), inputFile)
			} else {
				tidInt64, errConv := strconv.ParseInt(tid, 10, 64)
				if errConv != nil {
					log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Red(fmt.Sprintf("Invalid topic ID format for attachment: %s", tid)), au.Red(errConv))
					return fmt.Errorf("invalid topic ID %s for attachment: %w", tid, errConv)
				}
				docParams = tu.Document(tu.ID(tb.recipientId), inputFile).WithMessageThreadID(int(tidInt64))
			}

			if _, err := tb.api.SendDocument(tb.ctx, docParams); err != nil {
				targetDesc := "direct message"
				if tb.isChat { targetDesc = fmt.Sprintf("topic %s", tid) }
				log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Red(fmt.Sprintf("Failed to send attachment %s to %s (UID %d):", filename, targetDesc, data.Uid)), au.Red(err))
				return fmt.Errorf("failed to send attachment %s to %s (email UID %d) with Telego: %w", filename, targetDesc, data.Uid, err)
			}
			log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Green(fmt.Sprintf("Successfully sent attachment '%s' for UID %d.", filename, data.Uid)))
		}
		log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Green(fmt.Sprintf("All %d attachments for UID %d processed.", len(data.Attachments), data.Uid)))
	} else {
		log.Println(au.Gray(11, "[TELEGO_SENDMAIL]"), au.Cyan(fmt.Sprintf("No attachments to send for UID %d.", data.Uid)))
	}
	return nil
}

// Checkers
func (tb *TelegramBot) CheckAndRequestAdminRights(chatID int64) (bool, error) {
	log.Println(au.Gray(11, "[TELEGO_ADMIN]"), au.Yellow(fmt.Sprintf("Checking admin rights for bot in chat %d...", chatID)))
	if tb.api == nil {
		log.Println(au.Gray(11, "[TELEGO_ADMIN]"), au.Red("Telego API not initialized."))
		return false, errors.New("telego API is not initialized in CheckAndRequestAdminRights")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}
		tb.ctx = context.Background()
	} // Ensure context is not nil

	log.Println(au.Gray(11, "[TELEGO_ADMIN]"), au.Yellow("Getting bot's own user info (GetMe)..."))
	bu, err := tb.api.GetMe(tb.ctx)
	if err != nil {
		log.Println(au.Gray(11, "[TELEGO_ADMIN]"), au.Red("Failed to get bot info (GetMe):"), au.Red(err))
		return false, fmt.Errorf("failed to get bot info (GetMe): %w", err)
	}
	bid := bu.ID
	log.Println(au.Gray(11, "[TELEGO_ADMIN]"), au.Yellow(fmt.Sprintf("Getting chat member info for bot (ID: %d) in chat %d...", bid, chatID)))
	cm, err := tb.api.GetChatMember(tb.ctx, &telego.GetChatMemberParams{ChatID: tu.ID(chatID), UserID: bid})
	if err != nil {
		log.Println(au.Gray(11, "[TELEGO_ADMIN]"), au.Red("Failed to get chat member info:"), au.Red(err))
		return false, fmt.Errorf("failed to get chat member info: %w", err)
	}

	switch v := cm.(type) {
	case *telego.ChatMemberAdministrator, *telego.ChatMemberOwner:
		log.Println(au.Gray(11, "[TELEGO_ADMIN]"), au.Green(fmt.Sprintf("Bot has admin rights in chat %d (Status: %s).", chatID, v.MemberStatus())))
		return true, nil
	default:
		log.Println(au.Gray(11, "[TELEGO_ADMIN]"), au.Yellow(fmt.Sprintf("Bot does NOT have admin rights in chat %d (Status: %s).", chatID, v.MemberStatus())))
		return false, nil
	}
}

func (tb *TelegramBot) CheckTopicsEnabled(chatID int64) (bool, error) {
	log.Println(au.Gray(11, "[TELEGO_TOPIC_CHECK]"), au.Yellow(fmt.Sprintf("Checking if topics are enabled for chat ID %d...", chatID)))
	tb.isChat = false // Default to false
	if tb.api == nil {
		log.Println(au.Gray(11, "[TELEGO_TOPIC_CHECK]"), au.Red("Telego API not initialized."))
		return tb.isChat, errors.New("telego API not initialized in CheckTopicsEnabled")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}

	chat, errChat := tb.api.GetChat(tb.ctx, &telego.GetChatParams{ChatID: tu.ID(chatID)})
	if errChat != nil {
		log.Println(au.Gray(11, "[TELEGO_TOPIC_CHECK]"), au.Red(fmt.Sprintf("Failed to get chat details for %d:", chatID)), au.Red(errChat))
		return tb.isChat, fmt.Errorf("failed to get chat details for %d: %w", chatID, errChat)
	}

	if chat.IsForum {
		tb.isChat = true
		log.Println(au.Gray(11, "[TELEGO_TOPIC_CHECK]"), au.Green(fmt.Sprintf("Topics ARE enabled for chat ID %d (IsForum: true).", chatID)))
	} else {
		log.Println(au.Gray(11, "[TELEGO_TOPIC_CHECK]"), au.Yellow(fmt.Sprintf("Topics are NOT enabled for chat ID %d (IsForum: %v).", chatID, chat.IsForum)))
	}
	return tb.isChat, nil
}

// Events from user
func (t *TelegramBot) handleUpdate(update telego.Update, replayMessageFunc func(uid int, message string, files []struct{ Url, Name string }), newMessageFunc func(to string, title string, message string, files []struct{ Url, Name string })) {
	if t.ctx == nil {
		t.ctx = context.Background()
	}
	if update.Message == nil {
		// log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.BrightBlack("Received update with nil message, skipping."))
		return
	}
	msg := update.Message
	var fromUserID int64
	if msg.From != nil {
		fromUserID = msg.From.ID
	}

	log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Cyan(fmt.Sprintf("Received message in ChatID %d from UserID %d. Bot's RecipientID: %d.", msg.Chat.ID, fromUserID, t.recipientId)))

	if msg.Chat.ID != t.recipientId {
		log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Yellow(fmt.Sprintf("Ignoring message from unexpected chat: MessageChat.ID=%d. Expected recipientID: %d", msg.Chat.ID, t.recipientId)))
		return
	}

	log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Green(fmt.Sprintf("Processing message from ChatID %d (UserID %d).", msg.Chat.ID, fromUserID)))

	// Reply message
	if msg.ReplyToMessage != nil {
		log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Cyan("Message is a reply. Checking for embedded UID..."))
		repliedText := msg.ReplyToMessage.Text
		if repliedText == "" && msg.ReplyToMessage.Caption != "" {
			repliedText = msg.ReplyToMessage.Caption
		}

		if uidCode := telehtml.FindInvisibleIntSequences(repliedText); len(uidCode) > 0 {
			uidToReply := telehtml.DecodeIntInvisible(uidCode[0])
			log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Green(fmt.Sprintf("Found UID %d in replied message. Processing as email reply.", uidToReply)))

			if msg.MediaGroupID != "" {
				log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Cyan(fmt.Sprintf("Reply is part of a media group (ID: %s). Buffering...", msg.MediaGroupID)))
				if t.bufferAlbumMessage(msg, func(albumMsgs []*telego.Message) {
					log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Green(fmt.Sprintf("Album for media group %s complete. Processing reply for UID %d.", msg.MediaGroupID, uidToReply)))
					files := []struct{ Url, Name string }{}
					for _, m := range albumMsgs { files = append(files, t.getAllFileURLs(m)...) }
					replayMessageFunc(uidToReply, extractTextFromMessages(albumMsgs), files)
				}) {
					return // Buffered, will be processed later
				}
			}
			// Single file / non-album message for reply
			log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Cyan(fmt.Sprintf("Processing single message reply for UID %d.", uidToReply)))
			files := t.getAllFileURLs(msg)
			body := msg.Text
			if body == "" { body = msg.Caption }
			replayMessageFunc(uidToReply, body, files)
			return
		}
		log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Yellow("Reply detected, but no UID found in replied message. Treating as new message flow."))
	}

	// New Message or unhandled Reply
	log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Cyan("Processing as a potential new email message."))
	if msg.MediaGroupID != "" {
		log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Cyan(fmt.Sprintf("New message is part of a media group (ID: %s). Buffering...", msg.MediaGroupID)))
		if t.bufferAlbumMessage(msg, func(albumMsgs []*telego.Message) {
			log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Green(fmt.Sprintf("Album for media group %s complete. Processing as new email.", msg.MediaGroupID)))
			var rawText string
			for _, m := range albumMsgs { if m.Text != "" { rawText = m.Text; break }; if m.Caption != "" { rawText = m.Caption; break } }
			to, title, body, ok := parseMailContent(rawText)
			if !ok {
				log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Red("Invalid mail format in album message for new email."))
				// Optionally send help message here too
				return
			}
			files := []struct{ Url, Name string }{}
			for _, m := range albumMsgs { files = append(files, t.getAllFileURLs(m)...) }
			newMessageFunc(to, title, body, files)
		}) {
			return // Buffered
		}
	}

	// Single file / non-album new message
	log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Cyan("Processing as single new email message."))
	msgText := msg.Text
	if msgText == "" { msgText = msg.Caption }

	to, title, body, ok := parseMailContent(msgText)
	if !ok {
		log.Println(au.Gray(11, "[TELEGO_UPDATE]"), au.Yellow("Invalid mail format in single message. Sending help text."))
		// It's good practice to use the SendMessage method which has its own logging
		t.SendMessage("Hi! I'm your mail bot.") // These will be logged by SendMessage
		t.SendMessage("To reply to an email, just reply to the message and enter your text, and attach files if needed.")
		t.SendMessage("To send a new email, use the format:\n\nto.user@mail.example.com\nSubject line\nEmail text\n\nAttach files if needed.")
		return
	}
	files := t.getAllFileURLs(msg)
	newMessageFunc(to, title, body, files)
}

// Helpers
func (tb *TelegramBot) ensureTopic(subject string) (string, error) {
	log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Yellow(fmt.Sprintf("Ensuring topic exists for subject: '%s' in chat %d", subject, tb.recipientId)))
	if tb.api == nil {
		log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Red("Telego API not initialized in ensureTopic."))
		return "", errors.New("telego API not initialized in ensureTopic")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}

	params := &telego.CreateForumTopicParams{ChatID: tu.ID(tb.recipientId), Name: subject, IconColor: int(0x6FB9F0)}
	log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Yellow(fmt.Sprintf("Attempting to create forum topic with name: '%s'", subject)))
	forumTopic, err := tb.api.CreateForumTopic(tb.ctx, params)
	if err != nil {
		log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Red("Telego CreateForumTopic API error:"), au.Red(err))
		return "", fmt.Errorf("telego CreateForumTopic error: %w", err)
	}
	if forumTopic == nil {
		log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Red("Telego CreateForumTopic returned nil topic."))
		return "", errors.New("telego CreateForumTopic returned nil topic")
	}
	log.Println(au.Gray(11, "[TELEGO_TOPIC]"), au.Green(fmt.Sprintf("Successfully created/ensured topic. Name: '%s', ID: %d", forumTopic.Name, forumTopic.MessageThreadID)))
	return fmt.Sprint(forumTopic.MessageThreadID), nil
}

func (tb *TelegramBot) sendMessage(tid, text string) error {
	var targetDesc string
	if tid != "" {
		targetDesc = fmt.Sprintf("topic %s", tid)
	} else {
		targetDesc = fmt.Sprintf("chat %d (direct)", tb.recipientId)
	}
	log.Println(au.Gray(11, "[TELEGO_SEND_INTERNAL]"), au.Yellow(fmt.Sprintf("Sending message to %s. Length: %d, Preview: \"%s...\"", targetDesc, len(text), truncateMessage(text, 30))))

	p := tu.Message(tu.ID(tb.recipientId), text).WithParseMode(telego.ModeHTML).WithLinkPreviewOptions(&telego.LinkPreviewOptions{IsDisabled: true})
	if tid != "" {
		tidInt64, err := strconv.ParseInt(tid, 10, 64)
		if err != nil {
			log.Println(au.Gray(11, "[TELEGO_SEND_INTERNAL]"), au.Red(fmt.Sprintf("Invalid topic ID format: %s", tid)), au.Red(err))
			return fmt.Errorf("invalid topic ID format %s: %w", tid, err)
		}
		p.WithMessageThreadID(int(tidInt64))
	}

	if _, err := tb.api.SendMessage(tb.ctx, p); err != nil {
		log.Println(au.Gray(11, "[TELEGO_SEND_INTERNAL]"), au.Red(fmt.Sprintf("Failed to send message to %s:", targetDesc)), au.Red(err))
		return err
	}
	log.Println(au.Gray(11, "[TELEGO_SEND_INTERNAL]"), au.Green(fmt.Sprintf("Message successfully sent to %s.", targetDesc)))
	return nil
}

// truncateMessage helper for logging
func truncateMessage(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
