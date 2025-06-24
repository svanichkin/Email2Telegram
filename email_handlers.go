package main

import (
	"log"
	"sync"

	"github.com/logrusorgru/aurora/v4"
)

var mu sync.Mutex

func processNewEmails(ec *EmailClient, tb *TelegramBot, ai *OpenAIClient) {

	// Stopping idle mode

	mu.Lock()
	ec.imap.StopIdle()
	defer func() {
		if err := ec.startIdleWithHandler(); err != nil {
			tb.SendMessage("Failed to reply email for!")
			return
		}
		mu.Unlock()
	}()

	// Get new mail ids

	log.Println(au.Gray(12, "[EMAIL]").String() + " " + au.Cyan("Checking for new emails...").String())
	uids, err := ec.ListNewMailUIDs()
	if err != nil {
		log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Red("Error listing emails: %v").String(), err)
		return
	}

	// If first star, ignore all letters

	if uids, err = ec.AddAllUIDsIfFirstStart(uids); err != nil {
		log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Red("Error marking UIDs as processed on first start: %v").String(), err)
		return
	}

	// Main cycle for new letters

	for _, uid := range uids {
		m, err := ec.FetchMail(uid)
		if err != nil {
			log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Red("Error fetching email %d: %v").String(), uid, err)
			continue
		}
		d := ParseEmail(m, uid)

		if ai != nil && d != nil && d.TextBody != "" {
			log.Printf(au.Gray(12, "[OPENAI]").String()+" "+au.Magenta("Attempting to process email UID %d with OpenAI...").String(), uid)
			res, err := ai.GenerateTextFromEmail("Subject: " + d.Subject + " From: " + d.From + " To: " + d.To + " Body: " + d.TextBody)
			if err != nil {
				log.Printf(au.Gray(12, "[OPENAI]").String()+" "+au.Red("Failed to process email UID %d with OpenAI: %v. Sending original email.").String(), uid, err)
			}
			d.Type = res.Type
			d.Summary = res.Summary
			d.Unsubscrube = res.Unsubscribe
		}

		if err := tb.SendEmailData(d); err != nil {
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Red("Error sending email %d to Telegram: %v").String(), uid, err)
			continue
		}
		if err := ec.MarkUIDAsProcessed(uid); err != nil {
			log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Red("Error marking email %d as processed: %v").String(), uid, err)
		}
	}

}

func replayToEmail(ec *EmailClient, tb *TelegramBot, uid int, msg string, files []struct{ Url, Name string }) {

	mu.Lock()
	ec.imap.StopIdle()
	err := ec.ReplyTo(uid, msg, files)
	if err != nil {
		tb.SendMessage("Failed to reply email for!")
	}
	if err := ec.startIdleWithHandler(); err != nil {
		log.Fatalf(au.Gray(12, "[EMAIL]").String()+" "+au.Red(aurora.Bold("Failed to restart idle mode: %v")).String(), err)
	}
	mu.Unlock()

}

func sendNewEmail(ec *EmailClient, tb *TelegramBot, to, subj, msg string, files []struct{ Url, Name string }) {

	mu.Lock()
	ec.imap.StopIdle()
	err := ec.SendMail([]string{to}, subj, msg, files)
	if err != nil {
		tb.SendMessage("Failed to send email!")
	}
	if err := ec.startIdleWithHandler(); err != nil {
		log.Fatalf(au.Gray(12, "[EMAIL]").String()+" "+au.Red(aurora.Bold("Failed to restart idle mode: %v")).String(), err)
	}
	mu.Unlock()

}

func expandEmail(ec *EmailClient, tb *TelegramBot, uid int, tid int) {

	mu.Lock()
	ec.imap.StopIdle()
	m, err := ec.FetchMail(uid)
	if err != nil {
		tb.SendMessage("Failed to expand email!")
	}
	d := ParseEmail(m, uid)
	if err := tb.SendExpandEmailData(d, tid); err != nil {
		tb.SendMessage("Failed to expand email!")
	}
	if err := ec.startIdleWithHandler(); err != nil {
		log.Fatalf(au.Gray(12, "[EMAIL]").String()+" "+au.Red(aurora.Bold("Failed to restart idle mode: %v")).String(), err)
	}
	mu.Unlock()

}
