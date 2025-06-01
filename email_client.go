package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/mail"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	idle "github.com/emersion/go-imap-idle"
	"github.com/emersion/go-imap/client"
)

const processedUIDFile = "last_processed_uid.txt"

func loadLastProcessedUID(filePath string) (uint32, error) {

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to open UID file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
		val := strings.TrimSpace(scanner.Text())
		if val == "" {
			return 0, nil
		}
		uid64, err := strconv.ParseUint(val, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("invalid last UID: %w", err)
		}
		return uint32(uid64), nil
	}

	return 0, nil
}

func saveLastProcessedUID(filePath string, uid uint32) error {

	return os.WriteFile(filePath, []byte(fmt.Sprintf("%d\n", uid)), 0644)

}

// EmailClient holds the IMAP client connection and processed UIDs
type EmailClient struct {
	client *client.Client

	lastProcessedUID uint32
	dataMu           sync.Mutex

	host     string
	port     int
	username string
	password string

	connMu sync.Mutex
}

// NewEmailClient connects to the IMAP server and returns an EmailClient
func NewEmailClient(host string, port int, username string, password string) (*EmailClient, error) {

	serverAddr := fmt.Sprintf("%s:%d", host, port)
	log.Println("Connecting to IMAP server:", serverAddr)

	c, err := client.DialTLS(serverAddr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
	}
	c.SetDebug(os.Stdout)

	if err := c.Login(username, password); err != nil {
		c.Close()
		return nil, fmt.Errorf("failed to login: %w", err)
	}

	uid, err := loadLastProcessedUID(processedUIDFile)
	if err != nil {
		c.Logout()
		return nil, fmt.Errorf("failed to load processed UIDs: %w", err)
	}

	return &EmailClient{
		client:           c,
		lastProcessedUID: uid,
		host:             host,
		port:             port,
		username:         username,
		password:         password,
	}, nil

}

func (ec *EmailClient) Close() {

	if ec.client != nil {
		log.Println("Logging out...")
		err := ec.client.Logout()
		if err != nil {
			log.Printf("Error during logout: %v", err)
		} else {
			log.Println("Logged out successfully")
		}
	}

}

func (ec *EmailClient) ListNewMailUIDs() ([]uint32, error) {

	// ListNewMailUIDs fetches

	const timeoutSelect = 5 * time.Minute
	mbox, err := ec.selectInboxWithTimeout(timeoutSelect)
	if err != nil {
		log.Printf("SELECT INBOX failed (%v), attempting reconnect and retry", err)

		if reconErr := ec.reconnectIfNeeded(); reconErr != nil {
			return nil, fmt.Errorf("reconnect failed after select inbox error: %v (initial select error: %w)", reconErr, err)
		}
		mbox, err = ec.selectInboxWithTimeout(timeoutSelect)
		if err != nil {
			return nil, fmt.Errorf("select inbox failed again after reconnect: %w", err)
		}
	}
	log.Printf("INBOX selected, mailbox has %d messages", mbox.Messages)

	// Search new UIDs

	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.DeletedFlag}
	newUIDs, err := ec.uidSearchWithTimeout(criteria, timeoutSelect)
	if err != nil {
		log.Printf("UID search failed (%v), attempting reconnect and retry", err)
		if reconErr := ec.reconnectIfNeeded(); reconErr != nil {
			return nil, fmt.Errorf("reconnect failed after search error: %v (initial search error: %w)", reconErr, err)
		}
		newUIDs, err = ec.uidSearchWithTimeout(criteria, timeoutSelect)
		if err != nil {
			return nil, fmt.Errorf("UID search failed again after reconnect: %w", err)
		}
	}
	log.Printf("Found total %d UIDs in INBOX", len(newUIDs))

	ec.dataMu.Lock()
	defer ec.dataMu.Unlock()
	var unprocessed []uint32
	for _, uid := range newUIDs {
		if uid > ec.lastProcessedUID {
			unprocessed = append(unprocessed, uid)
		}
	}
	sort.Slice(unprocessed, func(i, j int) bool { return unprocessed[i] < unprocessed[j] })
	log.Printf("Found %d new unprocessed UIDs", len(unprocessed))

	return unprocessed, nil
}

func (ec *EmailClient) selectInboxWithTimeout(timeout time.Duration) (*imap.MailboxStatus, error) {

	resultChan := make(chan struct {
		mbox *imap.MailboxStatus
		err  error
	}, 1)

	go func() {
		mbox, err := ec.client.Select("INBOX", false)
		resultChan <- struct {
			mbox *imap.MailboxStatus
			err  error
		}{mbox, err}
	}()

	select {
	case res := <-resultChan:
		return res.mbox, res.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout reached in selectInboxWithTimeout after %v", timeout)
	}

}

func (ec *EmailClient) uidSearchWithTimeout(criteria *imap.SearchCriteria, timeout time.Duration) ([]uint32, error) {

	resultChan := make(chan struct {
		uids []uint32
		err  error
	}, 1)

	go func() {
		uids, err := ec.client.UidSearch(criteria)
		resultChan <- struct {
			uids []uint32
			err  error
		}{uids, err}
	}()

	select {
	case res := <-resultChan:
		return res.uids, res.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout occurred during UID SEARCH after %v", timeout)
	}

}

// FetchMail fetches the content of a specific email by UID.
func (ec *EmailClient) FetchMail(uid uint32) (*mail.Message, []byte, error) {

	if err := ec.reconnectIfNeeded(); err != nil {
		return nil, nil, err
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

	var msg *imap.Message
	select {
	case msg = <-messages:
		// ок
	case <-time.After(5 * time.Second):
		return nil, nil, fmt.Errorf("timeout waiting for message UID %d", uid)
	}
	if msg == nil {
		return nil, nil, fmt.Errorf("no message found for UID %d", uid)
	}
	bodyReader := msg.GetBody(section)
	if bodyReader == nil {
		return nil, nil, fmt.Errorf("could not find body for message UID %d", uid)
	}
	data, err := io.ReadAll(bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read body for UID %d: %w", uid, err)
	}
	parsedMail, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse email for UID %d: %w", uid, err)
	}

	return parsedMail, data, nil
}

func (ec *EmailClient) MarkUIDAsProcessed(uid uint32) error {

	ec.dataMu.Lock()
	if uid > ec.lastProcessedUID {
		ec.lastProcessedUID = uid
	}
	ec.dataMu.Unlock()

	return saveLastProcessedUID(processedUIDFile, ec.lastProcessedUID)
}

func (ec *EmailClient) AddAllUIDsIfFirstStart(uids []uint32) ([]uint32, error) {

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

func (ec *EmailClient) startIdle(onUpdate func()) error {
	// Проверка подключения, при необходимости переподключаемся
	if err := ec.reconnectIfNeeded(); err != nil {
		return fmt.Errorf("cannot connect via IMAP for IDLE: %w", err)
	}

	// Выбираем INBOX папку
	_, err := ec.client.Select("INBOX", false)
	if err != nil {
		return fmt.Errorf("failed to select INBOX: %w", err)
	}
	log.Println("[IDLE] INBOX selected, ready to enter IDLE mode.")

	idleClient := idle.NewClient(ec.client)
	stop := make(chan struct{})
	done := make(chan error, 1)

	// Канал для обновлений от IMAP-сервера
	updates := make(chan client.Update)
	ec.client.Updates = updates

	// Запускаем IDLE режим
	go func(stopCh chan struct{}) {
		log.Println("[IDLE] Entering IMAP IDLE mode; waiting for updates from server.")
		done <- idleClient.Idle(stopCh)
	}(stop)

	// Обработка событий и перезапуск IDLE
	go func() {
		for {
			select {

			// Получение обновлений от IMAP-сервера
			case update := <-updates:
				log.Printf("[IDLE] Received IMAP update: %v", update)
				if onUpdate != nil {
					onUpdate()
				}

			// Завершение IDLE режима с ошибкой или без
			case err := <-done:
				if err != nil {
					log.Printf("[IDLE] Error received during IDLE mode: %v", err)
				} else {
					log.Println("[IDLE] IDLE mode ended normally without error.")
				}
				return

			// Таймаут: перезапуск IDLE режима во избежание разрыва соединения
			case <-time.After(15 * time.Minute):
				log.Println("[IDLE] Restarting IDLE mode to prevent timeout.")
				close(stop)

				// Подождём секунду для корректного завершения текущего IDLE
				time.Sleep(time.Second)

				// Запускаем новый IDLE режим
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

func (ec *EmailClient) RunWithIdleAndTickerCallback(intervalSec int, shutdownChan <-chan struct{}, callback func()) error {

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

	// Тикер
	go func() {
		ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				log.Println("[Ticker] Triggered email check")
				processNewEmailsSafe()
			case <-shutdownChan:
				log.Println("Ticker goroutine shutting down")
				return
			}
		}
	}()

	// IDLE + callback
	err := ec.startIdle(func() {
		log.Println("[IDLE] Triggered email check")
		processNewEmailsSafe()
	})

	return err
}

// reconnectIfNeeded повторяет попытки до победного подключения каждые 15 секунд
func (ec *EmailClient) reconnectIfNeeded() error {
	ec.connMu.Lock()
	connected := ec.client != nil && (ec.client.State() == imap.AuthenticatedState || ec.client.State() == imap.SelectedState)
	ec.connMu.Unlock()

	if connected {
		return nil
	}

	return ec.reconnectWithRetries(0, 10*time.Second)
}

func (ec *EmailClient) reconnectWithRetries(maxAttempts int, initialDelay time.Duration) error {
	var lastErr error
	currentDelay := initialDelay
	for attempt := 1; maxAttempts <= 0 || attempt <= maxAttempts; attempt++ {
		ec.connMu.Lock()

		if ec.client != nil {
			ec.client.Close()
			ec.client = nil
		}

		serverAddr := fmt.Sprintf("%s:%d", ec.host, ec.port)
		log.Printf("Attempt [%d] reconnecting to IMAP server: %s", attempt, serverAddr)

		c, err := client.DialTLS(serverAddr, nil)
		if err == nil {
			err = c.Login(ec.username, ec.password)
			if err == nil {
				ec.client = c
				ec.connMu.Unlock()
				log.Println("Successfully reconnected.")
				return nil
			}
			c.Close()
		}

		lastErr = err
		ec.connMu.Unlock()

		log.Printf(
			"Reconnect attempt [%d] failed: %v. Retrying in %v...",
			attempt, err, currentDelay)

		time.Sleep(currentDelay)

		currentDelay *= 2
		if currentDelay > 5*time.Minute {
			currentDelay = 5 * time.Minute // макс. задержка
		}
	}

	return fmt.Errorf("unable to reconnect after %d attempts: %w", maxAttempts, lastErr)
}
