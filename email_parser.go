package main

import (
	"fmt"
	"strings"

	"github.com/svanichkin/go-imap"
	"github.com/svanichkin/TelegramHTML"
)

type ParsedEmailData struct {
	Uid         int
	From        string
	To          string
	Subject     string
	TextBody    string
	Attachments map[string][]byte
}

func ParseEmail(mail *imap.Email, uid int) *ParsedEmailData {

	// Compile fields

	data := &ParsedEmailData{
		Uid: uid,

		From: parseAddressList(mail.From),
		To:   parseAddressList(mail.To),

		Subject:     mail.Subject,
		TextBody:    mail.Text,
		Attachments: make(map[string][]byte),
	}

	if len(mail.Attachments) > 0 {
		for _, a := range mail.Attachments {
			data.Attachments[a.Name] = a.Content
		}
	}

	if mail.HTML != "" {
		data.TextBody = telehtml.CleanTelegramHTML(mail.HTML)
	}

	return data
}

func parseAddressList(m map[string]string) string {

	var result []string
	for address, name := range m {
		if name != "" {
			result = append(result, fmt.Sprintf("%s %s", address, name))
		} else {
			result = append(result, address)
		}
	}

	return strings.Join(result, ", ")
}
