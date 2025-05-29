package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/jhillyerd/enmime"
)

// ParsedEmailData holds extracted information from an email
type ParsedEmailData struct {
	Subject         string
	UnsubscribeLink string
	TextBody        string // Только plain text
	HTMLBody        string // Только HTML
	HasText         bool   // Явное указание наличия текста
	HasHTML         bool   // Явное указание наличия HTML
	PDFBody         []byte
	PDFName         string
	Attachments     map[string][]byte
}

func ParseEmail(raw []byte) (*ParsedEmailData, error) {
	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}

	data := &ParsedEmailData{
		Subject:         env.GetHeader("Subject"),
		UnsubscribeLink: extractUnsubscribeLink(env.GetHeader("List-Unsubscribe")),
		TextBody:        env.Text,
		HTMLBody:        env.HTML,
		HasText:         env.Text != "",
		HasHTML:         env.HTML != "",
		Attachments:     make(map[string][]byte),
	}

	// Обработка вложений
	for _, att := range env.Attachments {
		data.Attachments[att.FileName] = att.Content
	}

	// Генерация PDF
	if data.HasHTML {
		if pdf, err := ConvertHTMLToPDF(data.HTMLBody); err == nil {
			data.PDFName = SanitizeFileName(data.Subject) + ".pdf"
			data.PDFBody = pdf
		}
	}

	return data, nil
}

func SanitizeFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		// Запрещённые символы в Windows для имён файлов:
		// \ / : * ? " < > | и управляющие символы (r < 32)
		if r < 32 {
			continue
		}
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			continue
		}

		// Разрешаем буквы (любой язык), цифры, пробел, тире, подчёркивание, точку
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			continue
		}

		// Простая проверка для эмодзи (часть диапазонов)
		if (r >= 0x1F300 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x26FF) {
			b.WriteRune(r)
			continue
		}

		// Все остальные символы пропускаем
	}

	// Удаляем пробелы в начале и конце
	return strings.TrimSpace(b.String())
}

func ConvertHTMLToPDF(htmlContent string) ([]byte, error) {
	if htmlContent == "" {
		return nil, fmt.Errorf("HTML content is empty")
	}

	// Автоматический запуск браузера
	l := launcher.New().
		Headless(true).
		NoSandbox(true)

	url, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("failed to launch browser: %w", err)
	}

	browser := rod.New().ControlURL(url).MustConnect()
	defer browser.MustClose()

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("failed to create page: %w", err)
	}
	defer page.MustClose()

	// Устанавливаем контент страницы
	if err := page.SetDocumentContent(htmlContent); err != nil {
		return nil, fmt.Errorf("failed to set content: %w", err)
	}

	// 1. Ожидаем полной загрузки страницы
	page.WaitLoad()

	// 2. Явно ждем загрузки всех изображений
	page.Eval(`() => {
        return Promise.all(
            Array.from(document.images).map(img => {
                if (img.complete) return Promise.resolve();
                return new Promise((resolve) => {
                    img.addEventListener('load', resolve);
                    img.addEventListener('error', resolve);
                });
            })
        );
    }`)

	// 3. Добавляем дополнительную задержку для надежности
	time.Sleep(2 * time.Second)

	// Параметры PDF
	scale := 1.0
	paperWidth := 8.27
	paperHeight := 11.69

	pdfOpts := &proto.PagePrintToPDF{
		PrintBackground:   true,
		Scale:             &scale,
		PreferCSSPageSize: true,
		PaperWidth:        &paperWidth,
		PaperHeight:       &paperHeight,
	}

	// Генерация PDF
	pdfStream, err := page.PDF(pdfOpts)
	if err != nil {
		return nil, fmt.Errorf("PDF generation failed: %w", err)
	}

	// Чтение потока в байты
	pdfBytes, err := io.ReadAll(pdfStream)
	if err != nil {
		return nil, fmt.Errorf("failed to read PDF stream: %w", err)
	}

	return pdfBytes, nil
}

// Helper: Extract unsubscribe link
func extractUnsubscribeLink(header string) string {
	parts := strings.Split(header, ",")
	for _, part := range parts {
		clean := strings.TrimSpace(part)
		if strings.HasPrefix(clean, "<") && strings.HasSuffix(clean, ">") {
			url := clean[1 : len(clean)-1]
			if strings.HasPrefix(url, "http") {
				return url
			}
		}
	}
	return ""
}

/*import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"golang.org/x/net/html/charset"
)

// ParsedEmailData holds extracted information from an email
type ParsedEmailData struct {
	Subject         string
	UnsubscribeLink string
	TextBody        string // Только plain text
	HTMLBody        string // Только HTML
	HasText         bool   // Явное указание наличия текста
	HasHTML         bool   // Явное указание наличия HTML
	PDFBody         []byte
	Attachments     map[string][]byte
}

// decodeMessageBody decodes the entire message body based on Content-Transfer-Encoding
func decodeMessageBody(header mail.Header, body io.Reader) (io.Reader, error) {
	encoding := header.Get("Content-Transfer-Encoding")
	log.Printf("Top-level Content-Transfer-Encoding: %s", encoding)

	switch strings.ToLower(encoding) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body), nil
	case "quoted-printable":
		return quotedprintable.NewReader(body), nil
	case "7bit", "8bit", "binary", "":
		return body, nil
	default:
		return nil, fmt.Errorf("unsupported Content-Transfer-Encoding: %s", encoding)
	}
}

// Вспомогательная функция для создания указателей на float64
func float64Ptr(f float64) *float64 {
	return &f
}

func ConvertHTMLToPDF(htmlContent string) ([]byte, error) {
	if htmlContent == "" {
		return nil, fmt.Errorf("HTML content is empty")
	}

	// Автоматический запуск браузера
	l := launcher.New().
		Headless(true).
		NoSandbox(true)

	url, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("failed to launch browser: %w", err)
	}

	browser := rod.New().ControlURL(url).MustConnect()
	defer browser.MustClose()

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("failed to create page: %w", err)
	}
	defer page.MustClose()

	// Устанавливаем контент страницы
	if err := page.SetDocumentContent(htmlContent); err != nil {
		return nil, fmt.Errorf("failed to set content: %w", err)
	}

	// 1. Ожидаем полной загрузки страницы
	page.WaitLoad()

	// 2. Явно ждем загрузки всех изображений
	page.Eval(`() => {
        return Promise.all(
            Array.from(document.images).map(img => {
                if (img.complete) return Promise.resolve();
                return new Promise((resolve) => {
                    img.addEventListener('load', resolve);
                    img.addEventListener('error', resolve);
                });
            })
        );
    }`)

	// 3. Добавляем дополнительную задержку для надежности
	time.Sleep(2 * time.Second)

	// Параметры PDF
	scale := 1.0
	paperWidth := 8.27
	paperHeight := 11.69

	pdfOpts := &proto.PagePrintToPDF{
		PrintBackground:   true,
		Scale:             &scale,
		PreferCSSPageSize: true,
		PaperWidth:        &paperWidth,
		PaperHeight:       &paperHeight,
	}

	// Генерация PDF
	pdfStream, err := page.PDF(pdfOpts)
	if err != nil {
		return nil, fmt.Errorf("PDF generation failed: %w", err)
	}

	// Чтение потока в байты
	pdfBytes, err := io.ReadAll(pdfStream)
	if err != nil {
		return nil, fmt.Errorf("failed to read PDF stream: %w", err)
	}

	return pdfBytes, nil
}

// decodePartBody decodes multipart part body
func decodePartBody(part *multipart.Part) ([]byte, error) {
	encoding := strings.ToLower(part.Header.Get("Content-Transfer-Encoding"))
	log.Printf("Decoding part with encoding: %s", encoding)

	var reader io.Reader = part
	switch encoding {
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, part)
	case "quoted-printable":
		reader = quotedprintable.NewReader(part)
	case "7bit", "8bit", "binary":
	default:
		log.Printf("Unhandled encoding '%s', reading raw", encoding)
	}

	return io.ReadAll(reader)
}

// decodeText handles charset conversion
func decodeText(bodyBytes []byte, contentTypeHeader string) (string, error) {
	if len(bodyBytes) == 0 {
		return "", nil
	}

	charsetName := "utf-8"
	if contentTypeHeader != "" {
		_, params, err := mime.ParseMediaType(contentTypeHeader)
		if err == nil && params["charset"] != "" {
			charsetName = params["charset"]
		}
	}

	log.Printf("Decoding text with charset: %s", charsetName)
	encoding, _ := charset.Lookup(charsetName)
	if encoding != nil {
		decoded, err := encoding.NewDecoder().Bytes(bodyBytes)
		if err == nil {
			return string(decoded), nil
		}
		log.Printf("Charset decode error: %v", err)
	}

	// Fallback to UTF-8
	if utf8.Valid(bodyBytes) {
		return string(bodyBytes), nil
	}

	log.Printf("Invalid UTF-8 data, returning raw bytes")
	return string(bodyBytes), fmt.Errorf("charset decoding failed")
}

// ParseEmail processes email messages
// func ParseEmail(msg *mail.Message) (*ParsedEmailData, error) {
// 	data := &ParsedEmailData{
// 		Attachments: make(map[string][]byte),
// 	}

// 	// 1. Decode top-level transfer encoding
// 	decodedBody, err := decodeMessageBody(msg.Header, msg.Body)
// 	if err != nil {
// 		log.Printf("Body decode error: %v", err)
// 		decodedBody = msg.Body // Attempt to use raw body
// 	}

// 	// 2. Process headers
// 	data.Subject = msg.Header.Get("Subject")

// 	// Unsubscribe link processing
// 	if link := extractUnsubscribeLink(msg.Header.Get("List-Unsubscribe")); link != "" {
// 		data.UnsubscribeLink = link
// 	}

// 	// 3. Content processing
// 	contentType := msg.Header.Get("Content-Type")
// 	mediaType, params, _ := mime.ParseMediaType(contentType)

// 	if strings.HasPrefix(mediaType, "multipart/") {
// 		mr := multipart.NewReader(decodedBody, params["boundary"])
// 		for {
// 			part, err := mr.NextPart()
// 			if err == io.EOF {
// 				break
// 			}
// 			if err != nil {
// 				log.Printf("Part read error: %v", err)
// 				continue
// 			}

// 			// Process part
// 			err = processEmailPart(part, data)
// 			part.Close() // Immediate close after processing

// 			if err != nil {
// 				log.Printf("Part processing error: %v", err)
// 			}
// 		}
// 	} else {
// 		// Single part message
// 		bodyBytes, err := io.ReadAll(decodedBody)
// 		if err != nil {
// 			log.Printf("Body read error: %v", err)
// 		} else {
// 			processSinglePart(mediaType, bodyBytes, contentType, data)
// 		}
// 	}

// 	// 4. Generate PDF if HTML available
// 	if data.HTMLBody != "" {
// 		pdfBytes, err := ConvertHTMLToPDF(data.HTMLBody)
// 		if err != nil {
// 			log.Printf("PDF conversion failed: %v", err)
// 		} else {
// 			data.PDFBody = pdfBytes
// 		}
// 	}

// 	return data, nil
// }

func ParseEmail(msg *mail.Message) (*ParsedEmailData, error) {
	data := &ParsedEmailData{
		Attachments: make(map[string][]byte),
		HasText:     false,
		HasHTML:     false,
	}

	// 1. Обрабатываем заголовки
	data.Subject = msg.Header.Get("Subject")
	data.UnsubscribeLink = extractUnsubscribeLink(msg.Header.Get("List-Unsubscribe"))

	// 2. Определяем Content-Type
	contentType := msg.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		log.Printf("Warning: invalid Content-Type header: %v", err)
		mediaType = "text/plain" // Фолбэк
	}

	// 3. Обрабатываем тело письма
	if strings.HasPrefix(mediaType, "multipart/") {
		err = parseMultipartMessage(msg, data, params["boundary"])
	} else {
		err = parseSinglePartMessage(msg, data, mediaType)
	}

	if err != nil {
		return data, fmt.Errorf("failed to parse email body: %w", err)
	}

	// 4. Генерируем PDF если есть HTML
	if data.HasHTML && data.HTMLBody != "" {
		pdfBytes, err := ConvertHTMLToPDF(data.HTMLBody)
		if err != nil {
			log.Printf("Warning: PDF generation failed: %v", err)
		} else {
			data.PDFBody = pdfBytes
		}
	}

	return data, nil
}

func parseMultipartMessage(msg *mail.Message, data *ParsedEmailData, boundary string) error {
	mr := multipart.NewReader(msg.Body, boundary)

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("Error reading multipart: %v", err)
			continue
		}

		contentType := part.Header.Get("Content-Type")
		mediaType, _, _ := mime.ParseMediaType(contentType)

		// Обрабатываем вложения
		if filename := getAttachmentFilename(part); filename != "" {
			attachmentData, err := decodePartBody(part)
			if err != nil {
				log.Printf("Error decoding attachment: %v", err)
				continue
			}
			data.Attachments[filename] = attachmentData
			continue
		}

		// Обрабатываем текстовые части
		bodyBytes, err := decodePartBody(part)
		if err != nil {
			log.Printf("Error decoding part body: %v", err)
			continue
		}

		switch mediaType {
		case "text/plain":
			if !data.HasText { // Берем только первый текстовый блок
				data.TextBody = string(bodyBytes)
				data.HasText = true
			}
		case "text/html":
			if !data.HasHTML { // Берем только первый HTML блок
				data.HTMLBody = string(bodyBytes)
				data.HasHTML = true
			}
		}

		part.Close()
	}
	return nil
}

func parseSinglePartMessage(msg *mail.Message, data *ParsedEmailData, mediaType string) error {
	bodyBytes, err := io.ReadAll(msg.Body)
	if err != nil {
		return fmt.Errorf("failed to read message body: %w", err)
	}

	switch mediaType {
	case "text/plain":
		data.TextBody = string(bodyBytes)
		data.HasText = true
	case "text/html":
		data.HTMLBody = string(bodyBytes)
		data.HasHTML = true
	default:
		// Если тип неизвестен, пробуем определить автоматически
		if isHTML(bodyBytes) {
			data.HTMLBody = string(bodyBytes)
			data.HasHTML = true
		} else {
			data.TextBody = string(bodyBytes)
			data.HasText = true
		}
	}
	return nil
}

func getAttachmentFilename(part *multipart.Part) string {
	// 1. Проверяем Content-Disposition
	_, params, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if filename := params["filename"]; filename != "" {
		return filename
	}

	// 2. Проверяем name в Content-Type
	_, partParams, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if filename := partParams["name"]; filename != "" {
		return filename
	}

	// 3. Не является вложением
	return ""
}

func isHTML(content []byte) bool {
	// Простая проверка по наличию HTML тегов
	return bytes.Contains(content, []byte("<html")) ||
		bytes.Contains(content, []byte("<HTML")) ||
		bytes.Contains(content, []byte("<!DOCTYPE html"))
}

// Helper: Extract unsubscribe link
func extractUnsubscribeLink(header string) string {
	parts := strings.Split(header, ",")
	for _, part := range parts {
		clean := strings.TrimSpace(part)
		if strings.HasPrefix(clean, "<") && strings.HasSuffix(clean, ">") {
			url := clean[1 : len(clean)-1]
			if strings.HasPrefix(url, "http") {
				return url
			}
		}
	}
	return ""
}

// Helper: Process single part message
// func processSinglePart(mediaType string, body []byte, contentType string, data *ParsedEmailData) {
// 	decoded, _ := decodeText(body, contentType)

// 	switch mediaType {
// 	case "text/plain":
// 		data.TextBody = decoded
// 		data.HasText = true
// 	case "text/html":
// 		data.HTMLBody = decoded
// 		data.HasHTML = true
// 	default:
// 		filename := "attachment"
// 		if name, _, _ := mime.ParseMediaType(contentType); name != "" {
// 			filename = name
// 		}
// 		data.Attachments[filename] = body
// 	}
// }

// Helper: Process multipart part
// func processEmailPart(part *multipart.Part, data *ParsedEmailData) error {
// 	contentType := part.Header.Get("Content-Type")
// 	mediaType, _, _ := mime.ParseMediaType(contentType)

// 	// Handle content disposition
// 	disposition, params, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
// 	filename := params["filename"]
// 	if filename == "" {
// 		filename = fmt.Sprintf("attachment-%d", len(data.Attachments)+1)
// 	}

// 	bodyBytes, err := decodePartBody(part)
// 	if err != nil {
// 		return fmt.Errorf("part decode failed: %w", err)
// 	}

// 	switch {
// 	case disposition == "attachment" || (disposition == "inline" && filename != ""):
// 		data.Attachments[filename] = bodyBytes
// 		log.Printf("Stored attachment: %s (%d bytes)", filename, len(bodyBytes))

// 	case mediaType == "text/plain":
// 		decoded, _ := decodeText(bodyBytes, contentType)
// 		data.TextBody = decoded
// 		data.HasText = true

// 	case mediaType == "text/html":
// 		decoded, _ := decodeText(bodyBytes, contentType)
// 		data.HTMLBody = decoded
// 		data.HasHTML = true

// 	default:
// 		log.Printf("Skipping unhandled part: %s", mediaType)
// 	}

// 	return nil
// }
*/
