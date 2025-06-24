package main

import (
	"fmt"
	"log"
	"strconv"

	"github.com/mymmrac/telego"
	telehtml "github.com/svanichkin/TelegramHTML"
)

func (tb *TelegramBot) handleReplyMessage(msg *telego.Message, replayMessageFunc func(uid int, message string, files []struct{ Url, Name string })) {

	// Get uid marked message

	var uid int
	if msg.MessageThreadID > 0 {

		// Get uid from topic

		uid, _ = strconv.Atoi(tb.uids[fmt.Sprint(msg.MessageThreadID)])

	} else {

		// Get uid from message

		repliedText := msg.ReplyToMessage.Text
		if repliedText == "" && msg.ReplyToMessage.Caption != "" {
			repliedText = msg.ReplyToMessage.Caption
		}
		res := telehtml.FindInvisibleIntSequences(repliedText)
		if len(res) > 0 {
			uid = telehtml.DecodeIntInvisible(res[0])
		}
	}

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Processing reply to message UID %d").String(), uid)

	// Group files (album)

	if msg.MediaGroupID != "" {
		if tb.bufferAlbumMessage(msg, func(albumMsgs []*telego.Message) {
			files := []struct{ Url, Name string }{}
			for _, m := range albumMsgs {
				files = append(files, tb.getAllFileURLs(m)...)
			}
			text := extractTextFromMessages(albumMsgs)
			log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Magenta("Processing album reply with %d files").String(), len(files))
			replayMessageFunc(uid, text, files)
		}) {
			return
		}
		return
	}

	// Single file / non-album message

	files := tb.getAllFileURLs(msg)
	body := msg.Text
	if body == "" {
		body = msg.Caption
	}
	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Magenta("Processing single reply with %d files").String(), len(files))
	replayMessageFunc(uid, body, files)

}

func (tb *TelegramBot) handleNewMessage(msg *telego.Message, newMessageFunc func(to string, title string, message string, files []struct{ Url, Name string })) {

	// Triggered bot off

	if msg.From.IsBot {
		return
	}

	// Group files (album) for new message

	if msg.MediaGroupID != "" {
		if tb.bufferAlbumMessage(msg, func(albumMsgs []*telego.Message) {
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
				tb.sendInstructions()
				return
			}
			files := []struct{ Url, Name string }{}
			for _, m := range albumMsgs {
				files = append(files, tb.getAllFileURLs(m)...)
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
		tb.sendInstructions()
		return
	}

	files := tb.getAllFileURLs(msg)
	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Magenta("Processing new message with %d files").String(), len(files))
	newMessageFunc(to, title, body, files)
}

func (tb *TelegramBot) handleExpandMessage(msg *telego.Message, uid int, expandMessageFunc func(uid int, tid int)) {

	log.Printf(au.Gray(12, "[TELEGRAM]").String()+" "+au.Blue("Processing expand to message UID %d").String(), uid)
	expandMessageFunc(uid, msg.MessageThreadID)

}
