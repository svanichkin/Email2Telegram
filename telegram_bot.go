package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

type TelegramBot struct {
	api         *telego.Bot
	recipientId int64
	token       string
	updates     <-chan telego.Update
	isChat      bool
	tids        map[string]string
	uids        map[string]string
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

	// Init topics and uids

	rid := fmt.Sprint(recipientID)
	tids, err := LoadAndDecrypt(rid, rid+".top")
	if err != nil {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Failed to load topics: %v").String(), err)
		tids = make(map[string]string)
	}
	uids, err := LoadAndDecrypt(rid, rid+".uis")
	if err != nil {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Failed to load (unical mail id) uids: %v").String(), err)
		uids = make(map[string]string)
	}

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Green(au.Bold("Bot initialized successfully")).String())
	return &TelegramBot{
		api:         bot,
		recipientId: recipientID,
		token:       apiToken,
		updates:     updates,
		ctx:         context.Background(),
		tids:        tids,
		uids:        uids,
	}, nil

}

func (tb *TelegramBot) StartListener(replayMessage func(uid int, message string, files []struct{ Url, Name string }), newMessage func(to string, title string, message string, files []struct{ Url, Name string }), expandMessage func(uid, tid int)) {

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Cyan("Starting message listener...").String())
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}
	go func() {
		for update := range tb.updates {
			tb.handleUpdate(update, replayMessage, newMessage, expandMessage)
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

func (tb *TelegramBot) SendEmailData(d *ParsedEmailData) error {

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Cyan("Sending email data (UID: %d, type: %s)").String(), d.Uid, string(d.Type))
	if tb.api == nil {
		return errors.New("telego API is not initialized in SendEmailData")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}
	if d == nil {
		return errors.New("parsed email data is nil")
	}
	var err error
	var tid int
	if tb.isChat && d.Type != TypeSpam && d.Type != TypePhishing {
		tid, err = tb.createTopicAndGetId(d)
		if err != nil {
			return err
		}
	}
	switch d.Type {
	case TypeCode:
		if err := tb.sendCode(tid, d); err != nil {
			return err
		}
		return nil
	case TypeSpam, TypePhishing:
		if err := tb.sendSpamOrPhishing(d); err != nil {
			return err
		}
		return nil
	default:
		if err := tb.sendHumanOrNotificationOrUnknown(tid, d); err != nil {
			return err
		}
		if err := tb.sendAttachments(tid, d); err != nil {
			return err
		}
	}

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Green("Email data sent successfully").String())
	return nil
}

func (tb *TelegramBot) SendExpandEmailData(d *ParsedEmailData, tid int) error {

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Cyan("Sending expanded email data (UID: %d, type: %s)").String(), d.Uid, string(d.Type))
	if tb.api == nil {
		return errors.New("telego API is not initialized in SendEmailData")
	}
	if tb.ctx == nil {
		tb.ctx = context.Background()
	}
	if d == nil {
		return errors.New("parsed email data is nil")
	}
	if err := tb.sendExpand(tid, d); err != nil {
		return err
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

func (tb *TelegramBot) handleUpdate(update telego.Update, replayMessageFunc func(uid int, message string, files []struct{ Url, Name string }), newMessageFunc func(to string, title string, message string, files []struct{ Url, Name string }), expandMessageFunc func(uid, tid int)) {

	if tb.ctx == nil {
		tb.ctx = context.Background()
	}
	var msg *telego.Message
	switch {
	case update.Message != nil:
		msg = update.Message
	case update.CallbackQuery != nil && update.CallbackQuery.Message != nil:
		msg = update.CallbackQuery.Message.Message()
	default:
		return
	}
	var fid int64
	if msg.From != nil {
		fid = msg.From.ID
	}
	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Cyan("Processing update from chat %d, user %d").String(), msg.Chat.ID, fid)

	// Check if message is from a topic that's not the main one

	if msg.Chat.ID != tb.recipientId {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Ignoring message from unexpected chat (expected %d)").String(), tb.recipientId)
		return
	}

	// Handle expand message callback

	if strings.HasPrefix(update.CallbackQuery.Data, "expand:") {
		uid, err := strconv.Atoi(strings.TrimPrefix(update.CallbackQuery.Data, "expand:"))
		if err != nil {
			return
		}
		tb.handleExpandMessage(msg, uid, expandMessageFunc)
		return
	}

	// Handle reply messages or topic message

	if msg.ReplyToMessage != nil {
		tb.handleReplyMessage(msg, replayMessageFunc)
		return
	}

	// Handle new messages

	tb.handleNewMessage(msg, newMessageFunc)
}
