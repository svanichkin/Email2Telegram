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

	"github.com/BrianLeishman/go-imap"
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

	handler  *imap.IdleHandler
	callback func()
}

// Lifecycle

func NewEmailClient(imapHost string, imapPort int, smtpHost string, smtpPort int, username string, password string, callback func()) (*EmailClient, error) {

	// Load last process UID

	processedUIDFile = username
	uid, err := loadLastProcessedUID(processedUIDFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load processed UIDs: %w", err)
	}

	// Create IMAP client

	serverAddr := fmt.Sprintf("%s:%d", imapHost, imapPort)
	log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Cyan("Connecting to IMAP server: %s").String(), serverAddr)

	imap.RetryCount = 100
	// imap.Verbose = true
	c, err := imap.New(username, password, imapHost, imapPort)
	if err != nil {
		return nil, fmt.Errorf("failed to login to IMAP server: %w", err)
	}

	idleHandler := imap.IdleHandler{
		OnExists: func(event imap.ExistsEvent) {
			log.Println(au.Gray(12, "[EMAIL]").String()+" "+au.Green("New email arrived: %d").String(), event.MessageIndex)
			callback()
		},
		OnExpunge: func(event imap.ExpungeEvent) {
			log.Println(au.Gray(12, "[EMAIL]").String()+" "+au.Yellow("Email expunged: %d").String(), event.MessageIndex)
		},
		OnFetch: func(event imap.FetchEvent) {
			log.Println(au.Gray(12, "[EMAIL]").String()+" "+au.Blue("Email fetched: %d, UID: %d").String(), event.MessageIndex, event.UID)
		},
	}

	log.Println(au.Gray(12, "[EMAIL]").String() + " " + au.Green(au.Bold("Email client initialized successfully")).String())
	return &EmailClient{
		imap: c,

		lastProcessedUID: uid,

		imapHost: imapHost,
		imapPort: imapPort,
		smtpHost: smtpHost,
		smtpPort: smtpPort,
		username: username,
		password: password,

		handler:  &idleHandler,
		callback: callback,
	}, nil

}

func (ec *EmailClient) reconnectIfNeeded() error {

	// IMAP reconnect

	if !ec.imap.Connected {
		log.Println(au.Gray(12, "[EMAIL]").String() + " " + au.Yellow("Reconnecting to IMAP server...").String())
		if err := ec.imap.Reconnect(); err != nil {
			return err
		}
		log.Println(au.Gray(12, "[EMAIL]").String() + " " + au.Green("Reconnected successfully").String())
	}

	return nil

}

func (ec *EmailClient) Close() {

	// IMAP Close

	if ec.imap != nil {
		log.Println(au.Gray(12, "[EMAIL]").String() + " " + au.Cyan("Logging out from IMAP...").String())
		err := ec.imap.Close()
		if err != nil {
			log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Red("Error during logout: %v").String(), err)
		} else {
			log.Println(au.Gray(12, "[EMAIL]").String() + " " + au.Green("Logged out successfully").String())
		}
	}

}

// Listener

func (ec *EmailClient) startIdleWithHandler() error {

	folder := "INBOX"
	if err := ec.selectFolder(folder); err != nil {
		return err
	}

	return ec.imap.StartIdle(ec.handler)
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
	log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Cyan("Fetching email UID %d from %s").String(), uid, folder)
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
		log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Blue("Selecting folder: %s").String(), folder)
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
	log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Cyan("Searching for new UIDs in %s").String(), folder)
	newUIDs, err := ec.imap.GetUIDs("UID " + strconv.Itoa(ec.lastProcessedUID) + ":* UNDELETED")
	if err != nil {
		return nil, fmt.Errorf("UID search failed: %w", err)
	}
	log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Blue("Found total %d UIDs in %s").String(), len(newUIDs), folder)

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
	log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Green("Found %d new unprocessed UIDs").String(), len(unprocessed))

	return unprocessed, nil
}

func (ec *EmailClient) MarkUIDAsProcessed(uid int) error {

	ec.dataMu.Lock()
	if uid > ec.lastProcessedUID {
		ec.lastProcessedUID = uid
	}
	ec.dataMu.Unlock()
	log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Blue("Marking UID %d as processed").String(), uid)

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
		log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Green("Saved initial last UID %d to %s").String(), maxUID, processedUIDFile)

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
			log.Println(au.Gray(12, "[EMAIL]").String() + " " + au.Yellow("No existing UID file found, starting fresh").String())
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
		uid, err := strconv.Atoi(val)
		if err != nil {
			return 0, fmt.Errorf("invalid last UID: %w", err)
		}
		log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Blue("Loaded last processed UID: %d").String(), uid)
		return uid, nil
	}

	return 0, nil
}

func saveLastProcessedUID(filePath string, uid int) error {

	log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Cyan("Saving last processed UID %d to %s").String(), uid, filePath)
	return os.WriteFile(filePath, []byte(fmt.Sprintf("%d\n", uid)), 0600)

}

// Replay email

func (ec *EmailClient) ReplyTo(uid int, message string, files []struct{ Url, Name string }) error {

	// Reconnect if needed

	if err := ec.reconnectIfNeeded(); err != nil {
		return fmt.Errorf("failed to reconnect: %w", err)
	}
	log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Cyan("Preparing reply to email UID %d").String(), uid)

	// Get original mail

	m, err := ec.FetchMail(uid)
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

	addresses := m.From
	if len(m.ReplyTo) > 0 {
		addresses = m.ReplyTo
		log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Blue("Using Reply-To address %v instead of From %v").String(), m.ReplyTo, m.From)
	}
	var to []string
	for address := range addresses {
		to = append(to, address)
	}
	msg := getHTMLMsg(ec.username, to, m.Subject, body)

	// Send

	log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Magenta("Sending reply via SMTP to %v").String(), to)
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

	log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Green(au.Bold("Successfully sent reply to email %d")).String(), uid)
	return nil
}

func (ec *EmailClient) SendMail(to []string, title string, message string, files []struct{ Url, Name string }) error {

	log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Cyan("Preparing new email to %s").String(), strings.Join(to, ", "))
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

	log.Println(au.Gray(12, "[EMAIL]").String() + " " + au.Magenta("Sending email via SMTP").String())
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
	log.Printf(au.Gray(12, "[EMAIL]").String()+" "+au.Green(au.Bold("Successfully sent email to %s")).String(), strings.Join(to, ", "))

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
