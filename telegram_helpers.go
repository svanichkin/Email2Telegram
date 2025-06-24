package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
	telehtml "github.com/svanichkin/TelegramHTML"
)

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

func (tb *TelegramBot) createTopicAndGetId(data *ParsedEmailData) (tid string, err error) {

	rid := fmt.Sprint(tb.recipientId)

	// If topics is nil, try loading or create empty

	if tb.topics == nil {
		tb.topics, err = LoadAndDecrypt(rid, rid+".top")
		if err != nil {
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Failed to load topics: %v").String(), err)
			tb.topics = make(map[string]string)
		}
	}

	if tb.uids == nil {
		tb.uids, err = LoadAndDecrypt(rid, rid+".uis")
		if err != nil {
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Failed to load uids: %v").String(), err)
			tb.uids = make(map[string]string)
		}
	}

	// Check topic id from topics, then create topic if needed

	subj := cleanSubject(data.Subject)
	tid = tb.topics[subj]
	if tid == "" {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Creating new topic for: %s").String(), subj)
		tid, err = tb.ensureTopic(subj)
		if err != nil {
			return "", fmt.Errorf("topic handling error (ensureTopic failed): %w", err)
		}
		tb.topics[subj] = tid
		if err := EncryptAndSave(rid, rid+".top", tb.topics); err != nil {
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Failed to save topics: %v").String(), err)
		}
		tb.uids[tid] = fmt.Sprint(data.Uid)
		if err := EncryptAndSave(rid, rid+".uis", tb.uids); err != nil {
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Failed to save uids: %v").String(), err)
		}
	}

	return tid, err

}

func (tb *TelegramBot) sendMessage(tid, text, unsubscribeURL string) error {

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Magenta("Sending message to topic %s").String(), tid)
	p := tu.Message(tu.ID(tb.recipientId), text)
	p.ParseMode = telego.ModeHTML
	if len(tid) > 0 {
		tidInt64, err := strconv.ParseInt(tid, 10, 64)
		if err != nil {
			return err
		}
		p.MessageThreadID = int(tidInt64)
	}
	p.LinkPreviewOptions = &telego.LinkPreviewOptions{IsDisabled: true}
	if unsubscribeURL != "" {
		p.ReplyMarkup = &telego.InlineKeyboardMarkup{
			InlineKeyboard: [][]telego.InlineKeyboardButton{
				{
					telego.InlineKeyboardButton{
						Text: "🚫 UNSUBSCRIBE",
						URL:  unsubscribeURL,
					},
				},
			},
		}
	}
	if _, err := tb.api.SendMessage(tb.ctx, p); err != nil {
		return err
	}

	return nil
}

func (tb *TelegramBot) sendCode(tid string, d *ParsedEmailData) error {

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Blue("Sending code message").String())
	if !tb.isChat {
		if err := tb.sendMessage("", "🔑 <b>"+d.Subject+"\n\n"+d.From+"\n⤷ "+d.To+"</b>"+telehtml.EncodeIntInvisible(d.Uid), ""); err != nil {
			return fmt.Errorf("failed to send title message (code) with Telego: %w", err)
		}
	} else {
		if err := tb.sendMessage("", "<b>"+d.From+"\n⤷ "+d.To+"</b>"+telehtml.EncodeIntInvisible(d.Uid), ""); err != nil {
			return fmt.Errorf("failed to send title message (code) with Telego: %w", err)
		}
	}
	message := d.Summary
	if message == "" {
		message = d.TextBody
	}
	messages := telehtml.SplitTelegramHTML(message)
	for i, msg := range messages {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Sending code message part %d/%d").String(), i+1, len(messages))
		if err := tb.sendMessage(tid, msg, d.Unsubscrube); err != nil {
			return fmt.Errorf("failed to send code message to topic %s with Telego: %w", tid, err)
		}
	}

	return nil

}

func (tb *TelegramBot) sendSpamOrPhishing(d *ParsedEmailData) error {

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Blue("Sending spam message").String())
	message := d.Summary
	if message == "" {
		message = d.TextBody
	}
	messages := telehtml.SplitTelegramHTML("🚫 <b>" + d.Subject + "\n\n" + d.From + "\n⤷ " + d.To + "</b>\n\n" + message)
	for i, msg := range messages {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Sending spam or phising message part %d/%d").String(), i+1, len(messages))
		if err := tb.sendMessage("", msg+telehtml.EncodeIntInvisible(d.Uid), d.Unsubscrube); err != nil {
			return fmt.Errorf("failed to send code message with Telego: %w", err)
		}
	}

	return nil

}

func (tb *TelegramBot) sendHumanOrNotificationOrUnknown(tid string, d *ParsedEmailData) error {

	message := d.Summary
	if message == "" {
		message = d.TextBody
	}
	var messages []string
	if !tb.isChat {
		messages = telehtml.SplitTelegramHTML("✉️ <b>" + d.Subject + "\n\n" + d.From + "\n⤷ " + d.To + "</b>\n\n" + message)
		tid = ""
	} else {
		messages = telehtml.SplitTelegramHTML("<b>" + d.From + "\n⤷ " + d.To + "</b>\n\n" + message)
	}
	for i, msg := range messages {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Sending message part %d/%d").String(), i+1, len(messages))
		if err := tb.sendMessage(tid, msg+telehtml.EncodeIntInvisible(d.Uid), d.Unsubscrube); err != nil {
			return fmt.Errorf("failed to send main part with Telego: %w", err)
		}
	}

	return nil

}

func (tb *TelegramBot) sendAttachments(tid string, d *ParsedEmailData) error {

	if len(d.Attachments) > 0 {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Cyan("Sending %d attachments").String(), len(d.Attachments))
		for fn, b := range d.Attachments {
			if len(b) == 0 {
				log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Skipping empty attachment: %s").String(), fn)
				continue
			}
			f := tu.FileFromReader(bytes.NewReader(b), fn)
			var p *telego.SendDocumentParams
			if !tb.isChat {
				p = tu.Document(tu.ID(tb.recipientId), f)
			} else {
				tidInt64, err := strconv.ParseInt(tid, 10, 64)
				if err != nil {
					return err
				}
				p = tu.Document(tu.ID(tb.recipientId), f).WithMessageThreadID(int(tidInt64))
			}
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Sending attachment: %s (%d bytes)").String(), fn, len(b))
			if _, err := tb.api.SendDocument(tb.ctx, p); err != nil {
				target := "direct message"
				if tb.isChat {
					target = fmt.Sprintf("topic %s", tid)
				}
				return fmt.Errorf("failed to send attachment %s to %s (email UID %d) with Telego: %w", fn, target, d.Uid, err)
			}
		}
	}

	return nil

}

func (tb *TelegramBot) sendInstructions() {

	tb.SendMessage("Hi! I'm your mail bot.")
	tb.SendMessage("To reply to an email, just reply to the message and enter your text, and attach files if needed.")
	tb.SendMessage("To send a new email, use the format:\n\nto.user@mail.example.com\nSubject line\nEmail text\n\nAttach files if needed.")

}
