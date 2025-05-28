package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"golang.org/x/net/html/charset"
)

// ParsedEmailData holds extracted information from an email
type ParsedEmailData struct {
	Subject         string
	UnsubscribeLink string
	TextBody        string
	HTMLBody        string
	IsHTML          bool
	PDFBody         []byte // Will store the PDF version of HTMLBody
	Attachments     map[string][]byte
}

// ConvertHTMLToPDF converts HTML string content to PDF bytes using Rod.
func ConvertHTMLToPDF(htmlContent string) (pdfBytes []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			// Convert panic to error
			err = fmt.Errorf("recovered panic during Rod PDF conversion: %v", r)
			log.Printf("Error: %v", err) // Log the panic as an error
		}
	}()

	if htmlContent == "" {
		return nil, fmt.Errorf("HTML content is empty, cannot convert to PDF")
	}

	// Attempt to find a locally installed browser executable.
	// The launcher path can be manually set if needed, e.g., u := launcher.New().Bin("/path/to/chrome").MustLaunch()
	var browserPath string
	var err error
	browserPath, err = launcher.LookPath()
	if err != nil {
		log.Printf("Warning: Could not automatically find browser for Rod: %v. You might need to set CHROME_PATH or ensure Chrome/Chromium is in PATH.", err)
		// Fallback or specific path if auto-detection fails and you know where it is
		// For CI environments, this might be a fixed path.
		// browserPath = "/usr/bin/google-chrome" // Example if you know it's there
		// For now, let launcher try its defaults if LookPath fails.
	}

	l := launcher.New()
	if browserPath != "" {
		l = l.Bin(browserPath)
	}
	// Add --no-sandbox for running in Docker/CI environments if necessary
	// l.Set("no-sandbox")

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("failed to launch browser for Rod: %w", err)
	}

	browser := rod.New().ControlURL(controlURL).MustConnect()
	defer browser.MustClose()
	log.Println("Rod browser instance launched.")

	page, err := browser.Page(proto.TargetCreateTarget{URL: ""}) // Create a new blank page
	if err != nil {
		return nil, fmt.Errorf("failed to create page with Rod: %w", err)
	}
	defer page.MustClose()
	log.Println("Rod page created.")

	// Set content
	// Using MustSetDocumentContent is generally preferred for setting HTML.
	page.MustSetDocumentContent(htmlContent) // This can panic
	log.Println("HTML content set on Rod page.")

	// Generate PDF
	// Default PDF options are usually fine. Customize with proto.PagePrintToPDF{} if needed.
	pdfBytes, err = page.PDF(&proto.PagePrintToPDF{ // Assign to named return parameter
		PrintBackground: true, // Example: ensure background graphics are printed
	})
	if err != nil {
		return nil, fmt.Errorf("failed to generate PDF with Rod: %w", err) // err will be shadowed if not careful, but here it's fine.
	}
	log.Printf("Successfully converted HTML to PDF using Rod (size: %d bytes)", len(pdfBytes))

	return pdfBytes, nil // pdfBytes is already assigned, err is nil if we reach here
}

// decodePartBody decodes the body of a multipart.Part based on its Content-Transfer-Encoding.
func decodePartBody(part *multipart.Part) ([]byte, error) {
	encoding := part.Header.Get("Content-Transfer-Encoding")
	log.Printf("Decoding part with Content-Transfer-Encoding: %s", encoding)

	var reader io.Reader = part
	switch strings.ToLower(encoding) {
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, part)
	case "quoted-printable":
		reader = quotedprintable.NewReader(part)
	case "7bit", "8bit", "binary":
		// No decoding needed, but we read it the same way
		break
	default:
		log.Printf("Warning: Unhandled Content-Transfer-Encoding '%s', reading as is.", encoding)
	}

	body, err := ioutil.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read part body: %w", err)
	}
	return body, nil
}

// decodeText attempts to decode the given bytes using the provided charset.
// If charset is empty or decoding fails, it returns the original bytes and any error.
func decodeText(bodyBytes []byte, contentTypeHeader string) (string, error) {
	if len(bodyBytes) == 0 {
		return "", nil
	}

	var bodyCharset string
	if contentTypeHeader != "" {
		_, params, err := mime.ParseMediaType(contentTypeHeader)
		if err == nil && params["charset"] != "" {
			bodyCharset = params["charset"]
		}
	}

	if bodyCharset != "" {
		log.Printf("Attempting to decode text part with charset: %s", bodyCharset)
		encoding, _ := charset.Lookup(bodyCharset)
		if encoding != nil {
			utf8Reader, err := encoding.NewDecoder().Bytes(bodyBytes)
			if err == nil {
				return string(utf8Reader), nil
			}
			log.Printf("Failed to decode with charset %s: %v. Falling back to UTF-8 or raw.", bodyCharset, err)
		} else {
			log.Printf("Charset %s not found. Falling back to UTF-8 or raw.", bodyCharset)
		}
	}

	// Fallback: try to interpret as UTF-8, if not, return as is with a warning
	if strings.ValidUTF8(string(bodyBytes)) {
		return string(bodyBytes), nil
	}
	
	// If not valid UTF-8, it might be an error or a different encoding not specified.
	// For robustness, we might return the string as is with a note, or an error.
	log.Printf("Warning: Text part is not valid UTF-8 and charset decoding failed or was not possible.")
	// Return the string as is, but signal there might have been an issue.
	return string(bodyBytes), fmt.Errorf("text part is not valid UTF-8 and specified charset '%s' could not be used for decoding, or was not specified", bodyCharset)
}


// ParseEmail extracts relevant information from a *mail.Message
func ParseEmail(msg *mail.Message) (*ParsedEmailData, error) {
	data := &ParsedEmailData{
		Attachments: make(map[string][]byte),
	}

	// 1. Subject
	data.Subject = msg.Header.Get("Subject")

	// 2. Unsubscribe Link
	unsubscribeHeader := msg.Header.Get("List-Unsubscribe")
	if unsubscribeHeader != "" {
		// The header can contain multiple URLs, often enclosed in < >.
		// Example: <mailto:unsubscribe@example.com>, <http://www.example.com/unsubscribe>
		parts := strings.Split(unsubscribeHeader, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "<") && strings.HasSuffix(part, ">") {
				url := part[1 : len(part)-1]
				// Basic validation: should probably involve net/url.Parse
				if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "mailto:") {
					data.UnsubscribeLink = url
					break
				}
			}
		}
		log.Printf("Found List-Unsubscribe header: '%s', extracted link: '%s'", unsubscribeHeader, data.UnsubscribeLink)
	}

	// 3. Content Type and Body
	contentTypeHeader := msg.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentTypeHeader)
	if err != nil {
		log.Printf("Warning: Could not parse Content-Type header '%s': %v. Attempting to read body directly.", contentTypeHeader, err)
		// Try to read the body directly as plain text if content type parsing fails
		bodyBytes, readErr := ioutil.ReadAll(msg.Body)
		if readErr == nil {
			data.TextBody, _ = decodeText(bodyBytes, contentTypeHeader) // Attempt to decode with charset if available
		} else {
			log.Printf("Error reading message body when Content-Type parsing failed: %v", readErr)
		}
		return data, nil // Return what we have, or an error if critical parsing failed
	}

	log.Printf("Parsing email with Content-Type: %s, Media Type: %s", contentTypeHeader, mediaType)

	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(msg.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				log.Printf("Error reading multipart part: %v", err)
				continue // Skip this part
			}
			defer part.Close()

			partContentType := part.Header.Get("Content-Type")
			partMediaType, partParams, err := mime.ParseMediaType(partContentType)
			if err != nil {
				log.Printf("Error parsing part Content-Type '%s': %v", partContentType, err)
				continue
			}
			log.Printf("Processing part with Content-Type: %s, Media Type: %s", partContentType, partMediaType)


			// Check Content-Disposition
			disposition, dispParams, dispErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			isAttachment := false
			filename := ""
			if dispErr == nil && (disposition == "attachment" || disposition == "inline") {
				isAttachment = true
				filename = dispParams["filename"]
				if filename == "" && disposition == "inline" && partParams["name"] != "" {
					 filename = partParams["name"] // some emails use name in content-type for inline images
				}
				if filename == "" { // fallback if filename is empty
					filename = fmt.Sprintf("attachment_%d", len(data.Attachments)+1)
				}
			}


			if isAttachment {
				log.Printf("Part identified as attachment. Disposition: %s, Filename: %s", disposition, filename)
				attachmentBytes, decodeErr := decodePartBody(part)
				if decodeErr != nil {
					log.Printf("Error decoding attachment '%s': %v", filename, decodeErr)
					continue
				}
				data.Attachments[filename] = attachmentBytes
				log.Printf("Successfully decoded and stored attachment: %s (size: %d bytes)", filename, len(attachmentBytes))
			} else {
				// Not an attachment, so it's likely a body part (text, html, etc.)
				bodyBytes, decodeErr := decodePartBody(part)
				if decodeErr != nil {
					log.Printf("Error decoding body part: %v", decodeErr)
					continue
				}

				switch partMediaType {
				case "text/plain":
					data.TextBody, err = decodeText(bodyBytes, partContentType)
					if err != nil {
						log.Printf("Error decoding text/plain part: %v", err)
					} else {
						log.Printf("Stored text/plain part (decoded size: %d)", len(data.TextBody))
					}
				case "text/html":
					data.HTMLBody, err = decodeText(bodyBytes, partContentType)
					if err != nil {
						log.Printf("Error decoding text/html part: %v", err)
					} else {
						log.Printf("Stored text/html part (decoded size: %d)", len(data.HTMLBody))
					}
					data.IsHTML = true
				default:
					log.Printf("Skipping multipart part with unhandled media type: %s", partMediaType)
				}
			}
		}
	} else {
		// Single part message
		log.Printf("Message is single part. Media Type: %s", mediaType)
		// For single part, mail.Message.Body is already decoded for Content-Transfer-Encoding.
		// We just need to handle charset.
		bodyBytes, readErr := ioutil.ReadAll(msg.Body)
		if readErr != nil {
			return nil, fmt.Errorf("failed to read single part message body: %w", readErr)
		}

		decodedBody, decodeErr := decodeText(bodyBytes, contentTypeHeader)
		if decodeErr != nil {
			log.Printf("Error decoding single part body with charset: %v. Storing raw bytes as string.", decodeErr)
			// Fallback to raw string if charset decoding fails
			if mediaType == "text/plain" {
				data.TextBody = string(bodyBytes)
			} else if mediaType == "text/html" {
				data.HTMLBody = string(bodyBytes)
				data.IsHTML = true
			}
		} else {
			if mediaType == "text/plain" {
				data.TextBody = decodedBody
				log.Printf("Stored single part text/plain (decoded size: %d)", len(data.TextBody))
			} else if mediaType == "text/html" {
				data.HTMLBody = decodedBody
				data.IsHTML = true
				log.Printf("Stored single part text/html (decoded size: %d)", len(data.HTMLBody))
			} else {
				log.Printf("Single part message with unhandled media type: %s. Body stored as potential attachment.", mediaType)
				filename := "attachment_0"
				if params["name"] != "" {
					filename = params["name"]
				}
				data.Attachments[filename] = bodyBytes // Store raw bytes for non-text attachments
			}
		}
	}

	// Attempt to convert HTML to PDF if HTML body is present
	if data.IsHTML && data.HTMLBody != "" {
		log.Println("HTML body is present, attempting to convert to PDF using Rod...")
		pdfBytes, pdfErr := ConvertHTMLToPDF(data.HTMLBody)
		if pdfErr != nil {
			log.Printf("Warning: Failed to convert HTML to PDF using Rod: %v", pdfErr)
			// PDF is optional, so we don't stop processing if conversion fails.
		} else {
			data.PDFBody = pdfBytes
			log.Printf("Successfully converted HTML to PDF using Rod, stored %d bytes.", len(data.PDFBody))
		}
	}

	return data, nil
}
