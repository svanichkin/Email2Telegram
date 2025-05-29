package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/mail"
	"os"
	"strconv"
	"strings"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

const processedUIDsFile = "processed_uids.txt"

// loadProcessedUIDs reads UIDs from the given file.
func loadProcessedUIDs(filePath string) (map[uint32]bool, error) {
	uids := make(map[uint32]bool)
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return uids, nil // File doesn't exist, return empty map
		}
		return nil, fmt.Errorf("failed to open processed UIDs file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		uid, err := strconv.ParseUint(line, 10, 32)
		if err != nil {
			log.Printf("Warning: skipping invalid UID line in %s: %v", filePath, err)
			continue
		}
		uids[uint32(uid)] = true
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading processed UIDs file: %w", err)
	}
	log.Printf("Loaded %d processed UIDs from %s", len(uids), filePath)
	return uids, nil
}

// saveProcessedUID appends the given UID to the file.
func saveProcessedUID(filePath string, uid uint32) error {
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open processed UIDs file for writing: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(fmt.Sprintf("%d\n", uid)); err != nil {
		return fmt.Errorf("failed to write UID to processed UIDs file: %w", err)
	}
	log.Printf("Saved UID %d to %s", uid, filePath)
	return nil
}

// EmailClient holds the IMAP client connection and processed UIDs
type EmailClient struct {
	client        *client.Client
	processedUIDs map[uint32]bool
}

// NewEmailClient connects to the IMAP server and returns an EmailClient
func NewEmailClient(host string, port int, username string, password string) (*EmailClient, error) {
	serverAddr := fmt.Sprintf("%s:%d", host, port)

	log.Println("Connecting to IMAP server:", serverAddr)
	c, err := client.DialTLS(serverAddr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
	}
	log.Println("Connected to IMAP server")

	log.Println("Logging in with username:", username)
	if err := c.Login(username, password); err != nil {
		c.Close() // Ensure connection is closed on login failure
		return nil, fmt.Errorf("failed to login: %w", err)
	}
	log.Println("Logged in successfully")

	uids, err := loadProcessedUIDs(processedUIDsFile)
	if err != nil {
		c.Logout() // Logout if we can't load UIDs
		c.Close()  // Ensure connection is closed
		return nil, fmt.Errorf("failed to load processed UIDs: %w", err)
	}

	return &EmailClient{client: c, processedUIDs: uids}, nil
}

// Close logs out the client
func (ec *EmailClient) Close() {
	if ec.client != nil {
		log.Println("Logging out...")
		err := ec.client.Logout()
		if err != nil {
			log.Printf("Error during logout: %v", err)
		} else {
			log.Println("Logged out successfully")
		}
		// The underlying connection is typically closed by Logout,
		// but an explicit Close call can be added if specific client library versions require it.
		// For emersion/go-imap, Logout generally handles this.
	}
}

// ListNewMailUIDs fetches all UIDs from INBOX and filters out processed ones.
func (ec *EmailClient) ListNewMailUIDs() ([]uint32, error) {
	if ec.client == nil {
		return nil, fmt.Errorf("IMAP client is not connected")
	}

	_, err := ec.client.Select("INBOX", false)
	if err != nil {
		return nil, fmt.Errorf("failed to select INBOX: %w", err)
	}
	log.Println("Selected INBOX")

	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.DeletedFlag} // Optionally filter out already deleted messages
	allUIDs, err := ec.client.UidSearch(criteria)
	if err != nil {
		return nil, fmt.Errorf("failed to search for UIDs: %w", err)
	}
	log.Printf("Found %d UIDs in INBOX", len(allUIDs))

	var newUIDs []uint32
	for _, uid := range allUIDs {
		if !ec.processedUIDs[uid] {
			newUIDs = append(newUIDs, uid)
		}
	}
	log.Printf("Found %d new UIDs", len(newUIDs))
	return newUIDs, nil
}

// FetchMail fetches the content of a specific email by UID.
func (ec *EmailClient) FetchMail(uid uint32) (*mail.Message, []byte, error) {
	if ec.client == nil {
		return nil, nil, fmt.Errorf("IMAP client is not connected")
	}

	if ec.client.Mailbox() == nil || ec.client.Mailbox().Name != "INBOX" {
		log.Println("Selecting INBOX for FetchMail...")
		_, err := ec.client.Select("INBOX", false)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to select INBOX for fetch: %w", err)
		}
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{
		imap.FetchEnvelope,
		imap.FetchFlags,
		imap.FetchUid,
		imap.FetchRFC822Size,
		section.FetchItem(),
	}

	messages := make(chan *imap.Message, 1)
	if err := ec.client.UidFetch(seqset, items, messages); err != nil {
		return nil, nil, fmt.Errorf("failed to fetch email with UID %d: %w", uid, err)
	}

	msg := <-messages
	if msg == nil {
		return nil, nil, fmt.Errorf("no message found for UID %d", uid)
	}

	bodyReader := msg.GetBody(section)
	if bodyReader == nil {
		return nil, nil, fmt.Errorf("could not find body for message UID %d", uid)
	}

	// TeeReader сохраняет копию в буфер
	var buf bytes.Buffer
	tee := io.TeeReader(bodyReader, &buf)

	parsedMail, err := mail.ReadMessage(tee)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse email content for UID %d: %w", uid, err)
	}

	log.Printf("Successfully fetched and parsed email with UID %d, Subject: %s", uid, parsedMail.Header.Get("Subject"))
	return parsedMail, buf.Bytes(), nil
}

// MarkUIDAsProcessed adds the UID to the in-memory map and saves it to the file.
func (ec *EmailClient) MarkUIDAsProcessed(uid uint32) error {
	ec.processedUIDs[uid] = true
	if err := saveProcessedUID(processedUIDsFile, uid); err != nil {
		// If saving fails, we should ideally have a strategy:
		// - Remove from in-memory map to retry later?
		// - Log and continue, risking reprocessing if app restarts before next save?
		// For now, just log the error and return it.
		log.Printf("Failed to save UID %d as processed to file: %v", uid, err)
		return fmt.Errorf("failed to mark UID %d as processed in file: %w", uid, err)
	}
	log.Printf("Marked UID %d as processed.", uid)
	return nil
}
