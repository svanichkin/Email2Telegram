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
	"github.com/logrusorgru/aurora/v4"
)

// Reuse the global 'au' instance from main.go by declaring it here
// This assumes 'au' is initialized in main.go before this package's functions are called.
// For a larger application, a dedicated logging package or passing 'au' would be better.
var au aurora.Aurora

func init() {
	// Initialize a local 'au' if not already (e.g. for tests or standalone use of this package)
	// This is a simple way; main.go's `au` should ideally be the one used.
	// A better approach might be to have a SetAurora(instance) function.
	if au == nil {
		au = aurora.New(aurora.WithColors(true))
	}
}

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
	log.Println(au.Gray(11, "[IMAP]"), au.Yellow(fmt.Sprintf("Attempting to connect to IMAP server: %s", serverAddr)))

	imap.RetryCount = 100
	// imap.Verbose = true // This would need to be adapted if used with aurora
	c, err := imap.New(username, password, imapHost, imapPort)
	if err != nil {
		log.Println(au.Gray(11, "[IMAP]"), au.Red(aurora.Bold("Failed to login to IMAP server:")), au.Red(err))
		return nil, fmt.Errorf("failed to login to IMAP server: %w", err)
	}
	log.Println(au.Gray(11, "[IMAP]"), au.Green(fmt.Sprintf("Successfully connected to IMAP server: %s", serverAddr)))

	idleHandler := imap.IdleHandler{
		OnExists: func(event imap.ExistsEvent) {
			log.Println(au.Gray(11, "[IMAP_IDLE]"), au.Cyan("New email arrived."), au.Sprintf("MsgIndex: %d", event.MessageIndex))
			callback()
		},
		OnExpunge: func(event imap.ExpungeEvent) {
			log.Println(au.Gray(11, "[IMAP_IDLE]"), au.Magenta("Email expunged."), au.Sprintf("MsgIndex: %d", event.MessageIndex))
		},
		OnFetch: func(event imap.FetchEvent) {
			log.Println(au.Gray(11, "[IMAP_IDLE]"), au.BrightBlue("Email fetched."), au.Sprintf("MsgIndex: %d, UID: %d", event.MessageIndex, event.UID))
		},
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

		handler:  &idleHandler,
		callback: callback,
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
		log.Println(au.Gray(11, "[IMAP]"), au.Yellow("IMAP logging out..."))
		err := ec.imap.Close()
		if err != nil {
			log.Println(au.Gray(11, "[IMAP]"), au.Red("Error during IMAP logout:"), au.Red(err))
		} else {
			log.Println(au.Gray(11, "[IMAP]"), au.Green("IMAP logged out successfully"))
		}
	}

}

// Listener

func (ec *EmailClient) startIdleWithHandler() error {
	log.Println(au.Gray(11, "[IMAP_IDLE]"), au.Yellow("(Re)starting IDLE mode for INBOX..."))
	folder := "INBOX"
	if err := ec.selectFolder(folder); err != nil {
		// selectFolder logs its own errors
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
		log.Println(au.Gray(11, "[IMAP]"), au.Yellow(fmt.Sprintf("Selecting folder '%s'...", folder)))
		err := ec.imap.SelectFolder(folder)
		if err != nil {
			log.Println(au.Gray(11, "[IMAP]"), au.Red(fmt.Sprintf("Failed to select folder '%s':", folder)), au.Red(err))
			return fmt.Errorf("failed to select folder %s: %w", folder, err)
		}
		log.Println(au.Gray(11, "[IMAP]"), au.Green(fmt.Sprintf("Successfully selected folder '%s'.", folder)))
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
	searchCriteria := fmt.Sprintf("UID %d:* UNDELETED", ec.lastProcessedUID)
	log.Println(au.Gray(11, "[IMAP_UID]"), au.Yellow(fmt.Sprintf("Searching for UIDs with criteria: \"%s\"", searchCriteria)))
	newUIDs, err := ec.imap.GetUIDs(searchCriteria)
	if err != nil {
		log.Println(au.Gray(11, "[IMAP_UID]"), au.Red("UID search failed:"), au.Red(err))
		return nil, fmt.Errorf("UID search failed: %w", err)
	}
	log.Println(au.Gray(11, "[IMAP_UID]"), au.Green(fmt.Sprintf("Found %d total UIDs in %s matching criteria.", len(newUIDs), folder)))

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
	if len(unprocessed) > 0 {
		log.Println(au.Gray(11, "[IMAP_UID]"), au.Green(fmt.Sprintf("Found %d new unprocessed UIDs (older: %d).", len(unprocessed), ec.lastProcessedUID)))
	} else {
		log.Println(au.Gray(11, "[IMAP_UID]"), au.Cyan(fmt.Sprintf("No new unprocessed UIDs found (older: %d).", ec.lastProcessedUID)))
	}
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
		log.Println(au.Gray(11, "[IMAP_UID]"), au.Cyan(fmt.Sprintf("First start: Saved initial last UID %d to %s", maxUID, processedUIDFile)))

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
	originalFrom := mail.From // For logging
	if len(mail.ReplyTo) > 0 {
		addresses = mail.ReplyTo
		log.Println(au.Gray(11, "[EMAIL_REPLY]"), au.Cyan("Using Reply-To address(es):"), mail.ReplyTo, au.BrightBlack("instead of From:"), originalFrom)
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
		log.Println(au.Gray(11, "[SMTP]"), au.Red(aurora.Bold("SMTP error during reply:")) яйцо au.Red(err))
		return fmt.Errorf("smtp error: %s", err)
	}

	log.Println(au.Gray(11, "[EMAIL_REPLY]"), au.Green(fmt.Sprintf("Successfully sent reply to email UID %d (To: %s)", uid, strings.Join(to, ", "))))
	return nil
}

func (ec *EmailClient) SendMail(to []string, title string, message string, files []struct{ Url, Name string }) error {
	log.Println(au.Gray(11, "[EMAIL_SEND]"), au.Yellow(fmt.Sprintf("Preparing to send new email. To: %s, Title: %s", strings.Join(to, ", "), title)))
	var attachments string
	if len(files) > 0 {
		attachments += "<ul>"
		for _, f := range files {
			attachments += fmt.Sprintf(`<li><a href="%s">%s</a></li>`, f.Url, html.EscapeString(f.Name))
		}
		attachments += "</ul>"
		log.Println(au.Gray(11, "[EMAIL_SEND]"), au.Cyan(fmt.Sprintf("Added %d attachments to the email.", len(files))))
	}
	body := fmt.Sprintf("<p>%s</p>%s", html.EscapeString(message), attachments)
	msg := getHTMLMsg(ec.username, to, title, body)

	// Send
	log.Println(au.Gray(11, "[SMTP]"), au.Yellow(fmt.Sprintf("Sending email via SMTP to %s...", strings.Join(to, ", "))))
	err := smtp.SendMail(
		fmt.Sprintf("%s:%d", ec.smtpHost, ec.smtpPort),
		smtp.PlainAuth("", ec.username, ec.password, ec.smtpHost),
		ec.username,
		to,
		[]byte(msg),
	)
	if err != nil {
		log.Println(au.Gray(11, "[SMTP]"), au.Red(aurora.Bold("SMTP error during send mail:")), au.Red(err))
		return fmt.Errorf("smtp error: %s", err)
	}
	log.Println(au.Gray(11, "[EMAIL_SEND]"), au.Green(fmt.Sprintf("Successfully sent email to %s", strings.Join(to, ", "))))

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
