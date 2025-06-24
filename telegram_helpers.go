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

func (tb *TelegramBot) createTopicAndGetId(data *ParsedEmailData) (tid int, err error) {

	rid := fmt.Sprint(tb.recipientId)

	// Check topic id from topics, then create topic if needed

	subj := cleanSubject(data.Subject)
	t := tb.tids[subj]
	if t == "" {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Creating new topic for: %s").String(), subj)
		t, err = tb.ensureTopic(subj)
		if err != nil {
			return 0, fmt.Errorf("topic handling error (ensureTopic failed): %w", err)
		}
		tb.tids[subj] = t
		if err := EncryptAndSave(rid, rid+".top", tb.tids); err != nil {
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Failed to save topics: %v").String(), err)
		}
		tb.uids[t] = fmt.Sprint(data.Uid)
		if err := EncryptAndSave(rid, rid+".uis", tb.uids); err != nil {
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Yellow("Failed to save uids: %v").String(), err)
		}
	}
	tid, err = strconv.Atoi(t)
	if err != nil {
		log.Println("Ошибка конвертации:", err)
	}
	return

}

func (tb *TelegramBot) sendMessage(tid int, text, unsubscribe, uid string) error {

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Magenta("Sending message to topic %s").String(), tid)
	p := tu.Message(tu.ID(tb.recipientId), text)
	p.ParseMode = telego.ModeHTML
	p.MessageThreadID = tid
	p.LinkPreviewOptions = &telego.LinkPreviewOptions{IsDisabled: true}
	var buttons []telego.InlineKeyboardButton
	if unsubscribe != "" {
		buttons = append(buttons, telego.InlineKeyboardButton{
			Text: "🚫 UNSUBSCRIBE",
			URL:  unsubscribe,
		})
	}
	if uid != "" {
		buttons = append(buttons, telego.InlineKeyboardButton{
			Text:         "🧾 EXPAND",
			CallbackData: "expand:" + uid,
		})
	}
	if len(buttons) > 0 {
		p.ReplyMarkup = &telego.InlineKeyboardMarkup{
			InlineKeyboard: [][]telego.InlineKeyboardButton{
				buttons,
			},
		}
	}
	if _, err := tb.api.SendMessage(tb.ctx, p); err != nil {
		return err
	}

	return nil
}

func (tb *TelegramBot) sendCode(tid int, d *ParsedEmailData) error {

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Blue("Sending code message").String())
	if !tb.isChat {
		if err := tb.sendMessage(0, "🔑 <b>"+d.Subject+"\n\n"+d.From+"\n⤷ "+d.To+"</b>"+telehtml.EncodeIntInvisible(d.Uid), "", ""); err != nil {
			return fmt.Errorf("failed to send title message (code) with Telego: %w", err)
		}
	} else {
		if err := tb.sendMessage(0, "<b>"+d.From+"\n⤷ "+d.To+"</b>"+telehtml.EncodeIntInvisible(d.Uid), "", ""); err != nil {
			return fmt.Errorf("failed to send title message (code) with Telego: %w", err)
		}
	}
	m, uid := messageAndUid(d)
	messages := telehtml.SplitTelegramHTML(m)
	for i, msg := range messages {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Sending code message part %d/%d").String(), i+1, len(messages))
		u, e := "", ""
		if i == len(messages)-1 {
			u, e = d.Unsubscrube, uid
		}
		if err := tb.sendMessage(tid, msg, u, e); err != nil {
			return fmt.Errorf("failed to send code message to topic %s with Telego: %w", tid, err)
		}
	}

	return nil

}

func (tb *TelegramBot) sendSpamOrPhishing(d *ParsedEmailData) error {

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Blue("Sending spam message").String())
	m, uid := messageAndUid(d)
	messages := telehtml.SplitTelegramHTML("🚫 <b>" + d.Subject + "\n\n" + d.From + "\n⤷ " + d.To + "</b>\n\n" + m)
	for i, msg := range messages {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Sending spam or phising message part %d/%d").String(), i+1, len(messages))
		u, e := "", ""
		if i == len(messages)-1 {
			u, e = d.Unsubscrube, uid
		}
		if err := tb.sendMessage(0, msg+telehtml.EncodeIntInvisible(d.Uid), u, e); err != nil {
			return fmt.Errorf("failed to send code message with Telego: %w", err)
		}
	}

	return nil

}

func (tb *TelegramBot) sendHumanOrNotificationOrUnknown(tid int, d *ParsedEmailData) error {

	m, uid := messageAndUid(d)
	var messages []string
	if !tb.isChat {
		messages = telehtml.SplitTelegramHTML("✉️ <b>" + d.Subject + "\n\n" + d.From + "\n⤷ " + d.To + "</b>\n\n" + m)
	} else {
		messages = telehtml.SplitTelegramHTML("<b>" + d.From + "\n⤷ " + d.To + "</b>\n\n" + m)
	}
	for i, msg := range messages {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Sending message part %d/%d").String(), i+1, len(messages))
		u, e := "", ""
		if i == len(messages)-1 {
			u, e = d.Unsubscrube, uid
		}
		if err := tb.sendMessage(tid, msg+telehtml.EncodeIntInvisible(d.Uid), u, e); err != nil {
			return fmt.Errorf("failed to send main part with Telego: %w", err)
		}
	}

	return nil

}

func (tb *TelegramBot) sendExpand(tid int, d *ParsedEmailData) error {

	var messages []string
	if tid > 0 {
		messages = telehtml.SplitTelegramHTML("<b>" + d.From + "\n⤷ " + d.To + "</b>\n\n" + d.TextBody)
	} else {
		messages = telehtml.SplitTelegramHTML("🧾 <b>" + d.Subject + "\n\n" + d.From + "\n⤷ " + d.To + "</b>\n\n" + d.TextBody)
	}
	for i, msg := range messages {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Sending message part %d/%d").String(), i+1, len(messages))
		u := ""
		if i == len(messages)-1 {
			u = d.Unsubscrube
		}
		if err := tb.sendMessage(tid, msg+telehtml.EncodeIntInvisible(d.Uid), u, ""); err != nil {
			return fmt.Errorf("failed to send main part with Telego: %w", err)
		}
	}

	return nil

}

func (tb *TelegramBot) sendAttachments(tid int, d *ParsedEmailData) error {

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
				p = tu.Document(tu.ID(tb.recipientId), f).WithMessageThreadID(tid)
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

func messageAndUid(d *ParsedEmailData) (string, string) {

	if d.Summary != "" {
		return d.Summary, fmt.Sprint(d.Uid)
	}

	return d.TextBody, ""
}
