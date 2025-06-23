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

func (tb *TelegramBot) sendMessage(tid, text string) error {

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
	if _, err := tb.api.SendMessage(tb.ctx, p); err != nil {
		return err
	}

	return nil
}

func (tb *TelegramBot) sendCode(tid string, data *ParsedEmailData) error {

	if !tb.isChat {
		if err := tb.sendMessage("", "🔑 <b>"+data.Subject+"\n\n"+data.From+"\n⤷ "+data.To+"</b>"+telehtml.EncodeIntInvisible(data.Uid)); err != nil {
			return fmt.Errorf("failed to send title message (code) with Telego: %w", err)
		}
		if err := tb.sendMessage("", data.TextBody); err != nil {
			return fmt.Errorf("failed to send code message body with Telego: %w", err)
		}
	} else {
		if err := tb.sendMessage("", "<b>"+data.From+"\n⤷ "+data.To+"</b>"+telehtml.EncodeIntInvisible(data.Uid)); err != nil {
			return fmt.Errorf("failed to send title message (code) with Telego: %w", err)
		}
		if err := tb.sendMessage(tid, data.TextBody); err != nil {
			return fmt.Errorf("failed to send code message to topic %s with Telego: %w", tid, err)
		}
	}

	return nil

}

func (tb *TelegramBot) sendSpam(data *ParsedEmailData) error {

	log.Println(au.Gray(12, "[TELEGRAM]").String() + " " + au.Blue("Sending spam message").String())
	if err := tb.sendMessage("", "🚫 <b>"+data.Subject+"\n\n"+data.From+"\n⤷ "+data.To+"</b>\n\n"+data.TextBody+telehtml.EncodeIntInvisible(data.Uid)); err != nil {
		return fmt.Errorf("failed to send main part with Telego: %w", err)
	}

	return nil

}

func (tb *TelegramBot) sendText(tid string, data *ParsedEmailData) error {

	var messages []string
	if !tb.isChat {
		messages = telehtml.SplitTelegramHTML("✉️ <b>" + data.Subject + "\n\n" + data.From + "\n⤷ " + data.To + "</b>\n\n" + data.TextBody)
		tid = ""
	} else {
		messages = telehtml.SplitTelegramHTML("<b>" + data.From + "\n⤷ " + data.To + "</b>\n\n" + data.TextBody)
	}
	for i, msg := range messages {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Sending message part %d/%d").String(), i+1, len(messages))
		if err := tb.sendMessage(tid, msg+telehtml.EncodeIntInvisible(data.Uid)); err != nil {
			return fmt.Errorf("failed to send main part with Telego: %w", err)
		}
	}

	return nil

}

func (tb *TelegramBot) sendAttachments(tid string, data *ParsedEmailData) error {

	if len(data.Attachments) > 0 {
		log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Cyan("Sending %d attachments").String(), len(data.Attachments))
		for fn, b := range data.Attachments {
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
				return fmt.Errorf("failed to send attachment %s to %s (email UID %d) with Telego: %w", fn, target, data.Uid, err)
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
