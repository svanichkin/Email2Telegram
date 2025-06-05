package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"html"
	"log"
	"net/smtp"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/svanichkin/go-imap"
)

type EmailClient struct {
	imap *imap.Dialer

	lastProcessedUID int
	dataMu           sync.Mutex

	imapHost string
	imapPort int
	smtpHost string
	smtpPort int
	username string
	password string
}

// Lifecycle

func NewEmailClient(imapHost string, imapPort int, smtpHost string, smtpPort int, username string, password string) (*EmailClient, error) {

	// Load last process UID
	processedUIDFile = username
	uid, err := loadLastProcessedUID(processedUIDFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load processed UIDs: %w", err)
	}

	// Create IMAP client

	serverAddr := fmt.Sprintf("%s:%d", imapHost, imapPort)
	log.Printf("Try connecting to IMAP server: %s", serverAddr)

	imap.RetryCount = 100
	// imap.Verbose = true
	c, err := imap.New(username, password, imapHost, imapPort)
	if err != nil {
		return nil, fmt.Errorf("failed to login to IMAP server: %w", err)
	}

	return &EmailClient{
		imap: c,

		lastProcessedUID: uid,

		imapHost: imapHost,
		imapPort: imapPort,
		smtpHost: smtpHost,
		smtpPort: smtpPort,
		username: username,
		password: password,
	}, nil

}

func (ec *EmailClient) reconnectIfNeeded() error {

	// IMAP reconnect

	if !ec.imap.Connected {
		if err := ec.imap.Reconnect(); err != nil {
			return err
		}
	}

	return nil

}

func (ec *EmailClient) Close() {

	// IMAP Close

	if ec.imap != nil {
		log.Println("IMAP logging out...")
		err := ec.imap.Close()
		if err != nil {
			log.Printf("Error during logout: %v", err)
		} else {
			log.Println("Logged out successfully")
		}
	}

}

// Listener

func (ec *EmailClient) RunUpdateChecker(callback func()) error {
	var mu sync.Mutex
	isProcessing := false
	var idleHandler imap.IdleHandler
	processNewEmailsSafe := func() {
		mu.Lock()
		if isProcessing {
			mu.Unlock()
			return
		}
		isProcessing = true
		mu.Unlock()

		go func() {
			defer func() {
				mu.Lock()
				isProcessing = false
				mu.Unlock()
				if err := ec.startIdleWithHandler(&idleHandler); err != nil {
					log.Printf("Failed to restart IDLE: %v", err)
				}
			}()

			callback()
		}()
	}
	idleHandler = imap.IdleHandler{
		OnExists: func(event imap.ExistsEvent) {
			log.Println("[IDLE] New email arrived:", event.MessageIndex)
			ec.imap.StopIdle()
			processNewEmailsSafe()
		},
		OnExpunge: func(event imap.ExpungeEvent) {
			log.Println("[IDLE] Email expunged:", event.MessageIndex)
		},
		OnFetch: func(event imap.FetchEvent) {
			log.Println("[IDLE] Email fetched:", event.MessageIndex, event.UID)
		},
	}

	return ec.startIdleWithHandler(&idleHandler)
}

func (ec *EmailClient) startIdleWithHandler(handler *imap.IdleHandler) error {

	log.Println("(Re)starting IDLE mode")
	folder := "INBOX"
	if err := ec.selectFolder(folder); err != nil {
		return err
	}

	return ec.imap.StartIdle(handler)
}

// Helpers

func (ec *EmailClient) FetchMail(uid int) (*imap.Email, error) {

	// Reconnect if needed

	if err := ec.reconnectIfNeeded(); err != nil {
		return nil, err
	}

	// Fetch from INBOX

	folder := "INBOX"
	if err := ec.selectFolder(folder); err != nil {
		return nil, err
	}
	emails, err := ec.imap.GetEmails(int(uid))
	if err != nil {
		return nil, err
	}
	if len(emails) == 0 {
		return nil, fmt.Errorf("no mail in %s with uid: %d", folder, uid)
	}

	return emails[int(uid)], nil
}

func (ec *EmailClient) selectFolder(folder string) error {

	if ec.imap.Folder != folder {
		log.Println("Selecting " + folder + " for FetchMail...")
		err := ec.imap.SelectFolder(folder)
		if err != nil {
			return fmt.Errorf("failed to select "+folder+" for fetch: %w", err)
		}
	}

	return nil
}

// Work with UID

func (ec *EmailClient) ListNewMailUIDs() ([]int, error) {

	// Reconnect if needed

	if err := ec.reconnectIfNeeded(); err != nil {
		return nil, err
	}

	// Connect to INBOX

	folder := "INBOX"
	if err := ec.selectFolder(folder); err != nil {
		return nil, err
	}
	newUIDs, err := ec.imap.GetUIDs("UNDELETED")
	if err != nil {
		return nil, fmt.Errorf("UID search failed: %w", err)
	}
	log.Printf("Found total %d UIDs in %s", len(newUIDs), folder)

	// Unprocessed UIDs

	ec.dataMu.Lock()
	defer ec.dataMu.Unlock()
	var unprocessed []int
	for _, uid := range newUIDs {
		if uid > ec.lastProcessedUID {
			unprocessed = append(unprocessed, uid)
		}
	}
	sort.Slice(unprocessed, func(i, j int) bool { return unprocessed[i] < unprocessed[j] })
	log.Printf("Found %d new unprocessed UIDs", len(unprocessed))

	return unprocessed, nil
}

func (ec *EmailClient) MarkUIDAsProcessed(uid int) error {

	ec.dataMu.Lock()
	if uid > ec.lastProcessedUID {
		ec.lastProcessedUID = uid
	}
	ec.dataMu.Unlock()

	return saveLastProcessedUID(processedUIDFile, ec.lastProcessedUID)
}

func (ec *EmailClient) AddAllUIDsIfFirstStart(uids []int) ([]int, error) {

	if _, err := os.Stat(processedUIDFile); os.IsNotExist(err) && len(uids) > 0 {
		maxUID := uids[0]
		for _, u := range uids {
			if u > maxUID {
				maxUID = u
			}
		}
		err := saveLastProcessedUID(processedUIDFile, maxUID)

		if err != nil {
			return uids, fmt.Errorf("failed to save initial max UID: %w", err)
		}
		ec.dataMu.Lock()
		ec.lastProcessedUID = maxUID
		ec.dataMu.Unlock()
		log.Printf("Saved initial last UID %d to %s", maxUID, processedUIDFile)

		return nil, nil
	}
	return uids, nil
}

var processedUIDFile string

func loadLastProcessedUID(filePath string) (int, error) {

	// Open uid file

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to open UID file: %w", err)
	}
	defer file.Close()

	// Read uid

	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
		val := strings.TrimSpace(scanner.Text())
		if val == "" {
			return 0, nil
		}
		uid, err := strconv.ParseInt(val, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("invalid last UID: %w", err)
		}
		return int(uid), nil
	}

	return 0, nil
}

func saveLastProcessedUID(filePath string, uid int) error {

	return os.WriteFile(filePath, []byte(fmt.Sprintf("%d\n", uid)), 0600)

}

// Replay email

func (ec *EmailClient) ReplyTo(uid int, message string, files []struct{ Url, Name string }) error {

	// Reconnect if needed

	if err := ec.reconnectIfNeeded(); err != nil {
		return fmt.Errorf("failed to reconnect: %w", err)
	}

	// Get original mail

	mail, err := ec.FetchMail(uid)
	if err != nil {
		return fmt.Errorf("error fetching email %d: %w", uid, err)
	}

	// Make new mail

	var attachments string
	if len(files) > 0 {
		attachments += "<ul>"
		for _, f := range files {
			attachments += fmt.Sprintf(`<li><a href="%s">%s</a></li>`, f.Url, html.EscapeString(f.Name))
		}
		attachments += "</ul>"
	}
	body := fmt.Sprintf("<p>%s</p>%s", html.EscapeString(message), attachments)

	addresses := mail.From
	if len(mail.ReplyTo) > 0 {
		addresses = mail.ReplyTo
		log.Printf("Using Reply-To address %v instead of From %v", mail.ReplyTo, mail.From)
	}
	var to []string
	for address := range addresses {
		to = append(to, address)
	}
	msg := getHTMLMsg(ec.username, to, mail.Subject, body)

	// Send

	err = smtp.SendMail(
		fmt.Sprintf("%s:%d", ec.smtpHost, ec.smtpPort),
		smtp.PlainAuth("", ec.username, ec.password, ec.smtpHost),
		ec.username,
		to,
		[]byte(msg),
	)
	if err != nil {
		return fmt.Errorf("smtp error: %s", err)
	}

	log.Printf("Successfully sent reply to email %d", uid)
	return nil
}

func (ec *EmailClient) SendMail(to []string, title string, message string, files []struct{ Url, Name string }) error {

	var attachments string
	if len(files) > 0 {
		attachments += "<ul>"
		for _, f := range files {
			attachments += fmt.Sprintf(`<li><a href="%s">%s</a></li>`, f.Url, html.EscapeString(f.Name))
		}
		attachments += "</ul>"
	}
	body := fmt.Sprintf("<p>%s</p>%s", html.EscapeString(message), attachments)
	msg := getHTMLMsg(ec.username, to, title, body)

	// Send

	err := smtp.SendMail(
		fmt.Sprintf("%s:%d", ec.smtpHost, ec.smtpPort),
		smtp.PlainAuth("", ec.username, ec.password, ec.smtpHost),
		ec.username,
		to,
		[]byte(msg),
	)
	if err != nil {
		return fmt.Errorf("smtp error: %s", err)
	}
	log.Printf("Successfully sent email to %s", strings.Join(to, ", "))

	return nil
}

func getHTMLMsg(username string, to []string, subject, body string) string {
	return "From: " + username + "\n" +
		"To: " + strings.Join(to, ", ") + "\n" +
		"Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte("Re: "+subject)) + "?=\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"MIME-Version: 1.0\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte(body)) + "\r\n"
}

func getTextMsg(username string, to []string, subject, body string) string {
	return "From: " + username + "\n" +
		"To: " + strings.Join(to, ", ") + "\n" +
		"Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte("Re: "+subject)) + "?=\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"MIME-Version: 1.0\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte(body)) + "\r\n"
}
