package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"html"
	"log" // Добавлен импорт для кодирования заголовков
	"net/smtp"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	goimap "github.com/BrianLeishman/go-imap"
	"github.com/emersion/go-imap"
	idle "github.com/emersion/go-imap-idle"
	"github.com/emersion/go-imap/client"
)

type EmailClient struct {
	idle *client.Client
	imap *goimap.Dialer

	lastProcessedUID int
	dataMu           sync.Mutex

	imapHost string
	imapPort int
	smtpHost string
	smtpPort int
	username string
	password string
	connMu   sync.Mutex
}

// Lifecycle

func NewEmailClient(imapHost string, imapPort int, smtpHost string, smtpPort int, username string, password string) (*EmailClient, error) {

	// Load last process UID

	uid, err := loadLastProcessedUID(processedUIDFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load processed UIDs: %w", err)
	}

	// Create IDLE client

	idle, err := newIdleClient(imapHost, imapPort, username, password)
	if err != nil {
		return nil, err
	}

	// Create IMAP client

	goimap, err := newImapClient(imapHost, imapPort, username, password)
	if err != nil {
		return nil, err
	}

	return &EmailClient{
		idle: idle,
		imap: goimap,

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

	// IDLE reconnect

	ec.connMu.Lock()
	connected := ec.idle.State() == imap.AuthenticatedState || ec.idle.State() == imap.SelectedState
	ec.connMu.Unlock()
	if !connected {
		if err := ec.reconnectIdle(); err != nil {
			return err
		}
	}

	return nil

}

func newImapClient(host string, port int, username string, password string) (*goimap.Dialer, error) {

	serverAddr := fmt.Sprintf("%s:%d", host, port)
	log.Printf("Try connecting to IMAP server: %s", serverAddr)

	goimap.RetryCount = 0
	c, err := goimap.New(username, password, host, port)
	if err != nil {
		return nil, fmt.Errorf("failed to login to IMAP server: %w", err)
	}

	return c, nil
}

func newIdleClient(host string, port int, username string, password string) (*client.Client, error) {

	serverAddr := fmt.Sprintf("%s:%d", host, port)
	log.Printf("Try connecting to IDLE server: %s", serverAddr)

	c, err := client.DialTLS(serverAddr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
	}

	if err := c.Login(username, password); err != nil {
		c.Close()
		return nil, fmt.Errorf("failed to login to IMAP server: %w", err)
	}

	return c, nil
}

func (ec *EmailClient) reconnectIdle() error {

	ec.connMu.Lock()
	defer ec.connMu.Unlock()
	if ec.idle != nil {
		ec.idle.Close()
		ec.idle = nil
	}
	c, err := newIdleClient(ec.imapHost, ec.imapPort, ec.username, ec.password)
	if err != nil {
		return err
	}
	ec.idle = c

	return nil
}

func (ec *EmailClient) Close() {

	// IDLE Close

	if ec.idle != nil {
		log.Println("IDLE logging out...")
		err := ec.idle.Logout()
		if err != nil {
			log.Printf("Error during logout: %v", err)
		} else {
			log.Println("Logged out successfully")
		}
	}

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

func (ec *EmailClient) RunUpdateChecker(intervalSec int, callback func()) error {

	var mu sync.Mutex
	isProcessing := false
	processNewEmailsSafe := func() {
		mu.Lock()
		if isProcessing {
			mu.Unlock()
			return
		}
		isProcessing = true
		mu.Unlock()

		defer func() {
			mu.Lock()
			isProcessing = false
			mu.Unlock()
		}()

		callback()
	}

	// Tick event

	go func() {
		ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
		defer ticker.Stop()
		for range ticker.C { // Упрощен цикл
			log.Println("[Ticker] Triggered email check")
			processNewEmailsSafe()
		}
	}()

	// IDLE event

	err := ec.startIdle(func() {
		log.Println("[IDLE] Triggered email check")
		processNewEmailsSafe()
	})

	return err
}

func (ec *EmailClient) startIdle(onUpdate func()) error {

	// Reconnect if needed

	if err := ec.reconnectIfNeeded(); err != nil {
		return fmt.Errorf("cannot connect via IMAP for IDLE: %w", err)
	}

	// Select INBOX

	_, err := ec.idle.Select("INBOX", false)
	if err != nil {
		return fmt.Errorf("failed to select INBOX: %w", err)
	}
	log.Println("[IDLE] INBOX selected, ready to enter IDLE mode.")

	idleClient := idle.NewClient(ec.idle)
	stop := make(chan struct{})
	done := make(chan error, 1)

	// IMAP chan - добавлен буфер
	updates := make(chan client.Update, 100)
	ec.idle.Updates = updates

	// Start IDLE

	go func(stopCh chan struct{}) {
		log.Println("[IDLE] Entering IMAP IDLE mode; waiting for updates from server.")
		done <- idleClient.Idle(stopCh)
	}(stop)

	// Restart and parse IDLE

	go func() {
		for {
			select {
			case update := <-updates:
				log.Printf("[IDLE] Received IMAP update: %v", update)
				if onUpdate != nil {
					onUpdate()
				}
			case err := <-done:
				if err != nil {
					log.Printf("[IDLE] Error received during IDLE mode: %v", err)
				} else {
					log.Println("[IDLE] IDLE mode ended normally without error.")
				}
				return
			case <-time.After(15 * time.Minute):
				log.Println("[IDLE] Restarting IDLE mode to prevent timeout.")
				close(stop)
				time.Sleep(time.Second)

				// Restart IDLE

				stop = make(chan struct{})
				go func(stopCh chan struct{}) {
					log.Println("[IDLE] Re-entering IMAP IDLE mode; waiting again for updates.")
					done <- idleClient.Idle(stopCh)
				}(stop)
			}
		}
	}()

	return nil
}

// Helpers

func (ec *EmailClient) FetchMail(uid int) (*goimap.Email, error) {

	// Reconnect if needed

	if err := ec.reconnectIfNeeded(); err != nil {
		return nil, err
	}

	// Fetch from INBOX

	if ec.imap.Folder != "INBOX" {
		log.Println("Selecting INBOX for FetchMail...")
		err := ec.imap.SelectFolder("INBOX")
		if err != nil {
			return nil, fmt.Errorf("failed to select INBOX for fetch: %w", err)
		}
	}
	emails, err := ec.imap.GetEmails(int(uid))
	if err != nil {
		return nil, err
	}
	if len(emails) == 0 {
		return nil, fmt.Errorf("no mail in INBOX with uid: %d", uid)
	}

	return emails[int(uid)], nil
}

// Work with UID

func (ec *EmailClient) ListNewMailUIDs() ([]int, error) {

	// Reconnect if needed

	if err := ec.reconnectIfNeeded(); err != nil {
		return nil, err
	}

	// Connect to INBOX

	if ec.imap.Folder != "INBOX" {
		log.Println("Selecting INBOX for ListNewMailUIDs...")
		err := ec.imap.SelectFolder("INBOX")
		if err != nil {
			return nil, fmt.Errorf("failed to select INBOX for fetch: %w", err)
		}
	}
	newUIDs, err := ec.imap.GetUIDs("UNDELETED")
	if err != nil {
		return nil, fmt.Errorf("UID search failed: %w", err)
	}
	log.Printf("Found total %d UIDs in INBOX", len(newUIDs))

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

const processedUIDFile = "last_processed_uid.txt"

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

	return os.WriteFile(filePath, []byte(fmt.Sprintf("%d\n", uid)), 0644)

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
