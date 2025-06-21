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
	telehtml "github.com/svanichkin/TelegramHTML"
)

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

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Cyan("Initializing Telegram bot...").String())
	bot, err := telego.NewBot(apiToken, telego.WithDiscardLogger())
	if err != nil {
		return nil, fmt.Errorf("failed to create Telegram bot with Telego: %w", err)
	}

	// Get bot info

	botUser, err := bot.GetMe(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get bot info (GetMe) with Telego: %w", err)
	}
	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Green("Authorized as @%s").String(), botUser.Username)
	updates, err := bot.UpdatesViaLongPolling(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get updates channel from Telego: %w", err)
	}

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Green(au.Bold("Bot initialized successfully")).String())
	return &TelegramBot{
		api:         bot,
		recipientId: recipientID,
		token:       apiToken,
		updates:     updates,
		ctx:         context.Background(),
	}, nil

}

func (t *TelegramBot) StartListener(replayMessage func(uid int, message string, files []struct{ Url, Name string }), newMessage func(to string, title string, message string, files []struct{ Url, Name string })) {

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Cyan("Starting message listener...").String())
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

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Magenta("Sending message to %d").String(), tb.recipientId)
	message := tu.Message(
		tu.ID(tb.recipientId),
		msg,
	)
	_, err := tb.api.SendMessage(tb.ctx, message)
	if err != nil {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Red("Error sending message: %v").String(), err)
		return fmt.Errorf("failed to send message via Telego: %w", err)
	}

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Green("Message sent successfully").String())
	return nil

}

func (tb *TelegramBot) RequestUserInput(prompt string) (string, error) {

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Cyan("Requesting user input...").String())
	if tb.api == nil {
		return "", errors.New("telego API is not initialized")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}
	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Sending prompt: %s").String(), prompt)
	message := tu.Message(
		tu.ID(tb.recipientId),
		prompt,
	)
	if _, err := tb.api.SendMessage(tb.ctx, message); err != nil {
		return "", fmt.Errorf("failed to send prompt with Telego: %w", err)
	}
	log.Printf("Sent prompt to recipient ID %d: %s", tb.recipientId, prompt)
	if tb.updates == nil {
		var err error
		tb.updates, err = tb.api.UpdatesViaLongPolling(tb.ctx, nil)
		if err != nil {
			return "", fmt.Errorf("failed to re-initialize updates channel in RequestUserInput: %w", err)
		}
	}
	timeout := time.After(5 * time.Minute)
	for {
		select {
		case update, ok := <-tb.updates:
			if !ok {
				log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Red("Updates channel closed").String())
				return "", errors.New("updates channel closed")
			}
			if update.Message == nil {
				continue
			}
			if update.Message.Chat.ID == tb.recipientId {
				log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Green("Received reply: %s").String(), update.Message.Text)
				return update.Message.Text, nil
			}
			fromID := int64(0)
			if update.Message.From != nil {
				fromID = update.Message.From.ID
			}
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Ignored message from chat %d, user %d (expected %d)").String(), update.Message.Chat.ID, fromID, tb.recipientId)

		case <-timeout:
			log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Red("Input timeout").String())
			return "", errors.New("timeout waiting for input reply")
		}
	}

}

func (tb *TelegramBot) SendEmailData(data *ParsedEmailData, isCode, isSpam bool) error {

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Cyan("Sending email data (UID: %d, IsCode: %t, IsSpam: %t)").String(),
		data.Uid, isCode, isSpam)
	if tb.api == nil {
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

		if tb.topics == nil {
			tb.topics, err = LoadAndDecrypt(rid, rid+".top")
			if err != nil {
				log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Failed to load topics: %v").String(), err)
				tb.topics = make(map[string]string)
			}
		}

		// Check topic id from topics, then create topic if needed

		subj := cleanSubject(data.Subject)
		tid = tb.topics[subj]
		if tid == "" {
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Creating new topic for: %s").String(), subj)
			tid, err = tb.ensureTopic(subj)
			if err != nil {
				return fmt.Errorf("topic handling error (ensureTopic failed): %w", err)
			}
			tb.topics[subj] = tid
			if errSave := EncryptAndSave(rid, rid+".top", tb.topics); errSave != nil {
				log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Failed to save topics: %v").String(), errSave)
			}
		}
	}

	// if code message

	if isCode {
		if !tb.isChat {
			if err := tb.sendMessage(tid, "🔑 <b>"+data.Subject+"\n\n"+data.From+"\n⤷ "+data.To+"</b>"+telehtml.EncodeIntInvisible(data.Uid)); err != nil {
				return fmt.Errorf("failed to send title message (code) with Telego: %w", err)
			}
			if err := tb.sendMessage(tid, data.TextBody); err != nil {
				return fmt.Errorf("failed to send code message body with Telego: %w", err)
			}
		} else {
			if err := tb.sendMessage(tid, data.TextBody); err != nil {
				return fmt.Errorf("failed to send code message to topic %s with Telego: %w", tid, err)
			}
		}
		return nil
	}

	// Header + text, then split

	body := data.TextBody
	if !tb.isChat {
		header := "<b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>" + "\n\n"
		if isSpam {
			header = "🚫 " + header
		} else {
			header = "✉️ " + header
		}
		body = header + data.TextBody
	}
	messages := telehtml.SplitTelegramHTML(body + telehtml.EncodeIntInvisible(data.Uid))

	// Send messages

	for i, msg := range messages {
		if len(msg) > 0 {
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Sending message part %d/%d").String(), i+1, len(messages))
			if err := tb.sendMessage(tid, msg); err != nil {
				return fmt.Errorf("failed to send main part with Telego: %w", err)
			}
		}
	}

	// Send sttachments

	if len(data.Attachments) > 0 {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Cyan("Sending %d attachments").String(), len(data.Attachments))
		for filename, contentBytes := range data.Attachments {
			if len(contentBytes) == 0 {
				log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Skipping empty attachment: %s").String(), filename)
				continue
			}
			inputFile := tu.FileFromReader(bytes.NewReader(contentBytes), filename)

			var docParams *telego.SendDocumentParams

			if !tb.isChat {
				docParams = tu.Document(tu.ID(tb.recipientId), inputFile)
			} else {
				tidInt64, err := strconv.ParseInt(tid, 10, 64)
				if err != nil {
					return err
				}
				docParams = tu.Document(tu.ID(tb.recipientId), inputFile).
					WithMessageThreadID(int(tidInt64))
			}
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Sending attachment: %s (%d bytes)").String(), filename, len(contentBytes))
			if _, err := tb.api.SendDocument(tb.ctx, docParams); err != nil {
				target := "direct message"
				if tb.isChat {
					target = fmt.Sprintf("topic %s", tid)
				}
				return fmt.Errorf("failed to send attachment %s to %s (email UID %d) with Telego: %w", filename, target, data.Uid, err)
			}
		}
	}

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Green("Email data sent successfully").String())
	return nil
}

// Checkers

func (tb *TelegramBot) CheckAndRequestAdminRights(chatID int64) (bool, error) {

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Cyan("Checking admin rights for chat %d").String(), chatID)
	if tb.api == nil {
		return false, errors.New("telego API is not initialized in CheckAndRequestAdminRights")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}
	bu, err := tb.api.GetMe(tb.ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get bot info (GetMe): %w", err)
	}
	bid := bu.ID
	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Checking admin status for bot @%s (ID: %d)").String(), bu.Username, bu.ID)
	cm, err := tb.api.GetChatMember(tb.ctx, &telego.GetChatMemberParams{
		ChatID: tu.ID(chatID),
		UserID: bid,
	})
	if err != nil {
		return false, fmt.Errorf("failed to get chat member info: %w", err)
	}
	switch v := cm.(type) {
	case *telego.ChatMemberAdministrator, *telego.ChatMemberOwner:
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Green("Bot has admin rights (status: %s)").String(), v.MemberStatus())
		return true, nil
	default:
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Bot lacks admin rights (status: %s)").String(), v.MemberStatus())
		return false, nil
	}

}

func (tb *TelegramBot) CheckTopicsEnabled(chatID int64) (bool, error) {

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Cyan("Checking topics for chat %d").String(), chatID)
	tb.isChat = false
	if tb.api == nil {
		return tb.isChat, errors.New("telego API not initialized in CheckTopicsEnabled")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}
	chat, errChat := tb.api.GetChat(tb.ctx, &telego.GetChatParams{ChatID: tu.ID(chatID)})
	if errChat != nil {
		return tb.isChat, fmt.Errorf("failed to get chat details for %d: %w", chatID, errChat)
	}
	if chat.IsForum {
		tb.isChat = true
		log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Green("Topics enabled (forum chat)").String())
		return tb.isChat, nil
	}

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Yellow("Topics disabled (not a forum)").String())
	return tb.isChat, nil

}

// Events from user

func (t *TelegramBot) handleUpdate(update telego.Update, replayMessageFunc func(uid int, message string, files []struct{ Url, Name string }), newMessageFunc func(to string, title string, message string, files []struct{ Url, Name string })) {

	if t.ctx == nil {
		t.ctx = context.Background()
	}
	if update.Message == nil {
		return
	}
	msg := update.Message
	var fid int64
	if msg.From != nil {
		fid = msg.From.ID
	}
	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Cyan("Processing update from chat %d, user %d").String(), msg.Chat.ID, fid)
	if msg.Chat.ID != t.recipientId {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Ignoring message from unexpected chat (expected %d)").String(), t.recipientId)
		return
	}

	// Reply message

	if msg.ReplyToMessage != nil {
		repliedText := msg.ReplyToMessage.Text

		if repliedText == "" && msg.ReplyToMessage.Caption != "" {
			repliedText = msg.ReplyToMessage.Caption
		}

		if uidCode := telehtml.FindInvisibleIntSequences(repliedText); len(uidCode) > 0 {
			uidToReply := telehtml.DecodeIntInvisible(uidCode[0])
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Processing reply to message UID %d").String(), uidToReply)

			// Group files (album)

			if msg.MediaGroupID != "" {
				if t.bufferAlbumMessage(msg, func(albumMsgs []*telego.Message) {
					files := []struct{ Url, Name string }{}
					for _, m := range albumMsgs {
						files = append(files, t.getAllFileURLs(m)...)
					}
					text := extractTextFromMessages(albumMsgs)
					log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Magenta("Processing album reply with %d files").String(), len(files))
					replayMessageFunc(uidToReply, text, files)
				}) {
					return
				}
			}

			// Single file / non-album message

			files := t.getAllFileURLs(msg)
			body := msg.Text
			if body == "" {
				body = msg.Caption
			}
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Magenta("Processing single reply with %d files").String(), len(files))
			replayMessageFunc(uidToReply, body, files)
			return
		}
	}

	// New Message or unhandled Reply
	// Group files (album) for new message

	if msg.MediaGroupID != "" {
		if t.bufferAlbumMessage(msg, func(albumMsgs []*telego.Message) {
			var rawText string
			for _, m := range albumMsgs {
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
				log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Yellow("Invalid mail format in album").String())
				return
			}
			files := []struct{ Url, Name string }{}
			for _, m := range albumMsgs {
				files = append(files, t.getAllFileURLs(m)...)
			}
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Magenta("Processing new album message with %d files").String(), len(files))
			newMessageFunc(to, title, body, files)
		}) {
			return
		}
	}

	// Single file / non-album new message

	msgText := msg.Text
	if msgText == "" {
		msgText = msg.Caption
	}

	to, title, body, ok := parseMailContent(msgText)
	if !ok {
		log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Yellow("Invalid mail format, sending instructions").String())
		t.SendMessage("Hi! I'm your mail bot.")
		t.SendMessage("To reply to an email, just reply to the message and \n\nenter your text, and attach files if needed.")
		t.SendMessage("To send a new email, use the format:\n\nto.user@mail.example.com\nSubject line\nEmail text\n\nAttach files if needed.")
		return
	}
	files := t.getAllFileURLs(msg)
	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Magenta("Processing new message with %d files").String(), len(files))
	newMessageFunc(to, title, body, files)

}

// Helpers

func (tb *TelegramBot) ensureTopic(subject string) (string, error) {

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Cyan("Ensuring topic exists: %s").String(), subject)
	if tb.api == nil {
		return "", errors.New("telego API not initialized in ensureTopic")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}
	params := &telego.CreateForumTopicParams{
		ChatID:    tu.ID(tb.recipientId),
		Name:      subject,
		IconColor: int(0x6FB9F0),
	}
	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Blue("Creating new forum topic").String())
	forumTopic, err := tb.api.CreateForumTopic(tb.ctx, params)
	if err != nil {
		return "", fmt.Errorf("telego CreateForumTopic error: %w", err)
	}
	if forumTopic == nil {
		return "", errors.New("telego CreateForumTopic returned nil topic")
	}

	tid := fmt.Sprint(forumTopic.MessageThreadID)
	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Green("Created topic %s with ID %s").String(), subject, tid)
	return tid, nil

}

func (tb *TelegramBot) sendMessage(tid, text string) error {

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Magenta("Sending message to topic %s").String(), tid)
	p := tu.Message(tu.ID(tb.recipientId), text)
	p.ParseMode = telego.ModeHTML
	tidInt64, err := strconv.ParseInt(tid, 10, 64)
	if err != nil {
		return err
	}
	p.MessageThreadID = int(tidInt64)
	p.LinkPreviewOptions = &telego.LinkPreviewOptions{IsDisabled: true}
	if _, err := tb.api.SendMessage(tb.ctx, p); err != nil {
		return err
	}

	return nil
}
