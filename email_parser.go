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

// ConvertHTMLToPDF converts HTML string content to PDF bytes using wkhtmltopdf.
func ConvertHTMLToPDF(htmlContent string) ([]byte, error) {
	if htmlContent == "" {
		return nil, fmt.Errorf("HTML content is empty, cannot convert to PDF")
	}

	pdfg, err := wkhtmltopdf.NewPdfGenerator()
	if err != nil {
		return nil, fmt.Errorf("failed to create PDF generator: %w", err)
	}

	// Add a page from a string.
	pdfg.AddPage(wkhtmltopdf.NewPageReader(strings.NewReader(htmlContent)))

	// Create PDF document in internal buffer
	err = pdfg.Create()
	if err != nil {
		return nil, fmt.Errorf("failed to create PDF: %w", err)
	}

	pdfBytes, err := pdfg.Bytes()
	if err != nil {
		return nil, fmt.Errorf("failed to get PDF bytes: %w", err)
	}

	log.Printf("Successfully converted HTML to PDF (size: %d bytes)", len(pdfBytes))
	return pdfBytes, nil
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
		log.Println("HTML body is present, attempting to convert to PDF...")
		pdfBytes, pdfErr := ConvertHTMLToPDF(data.HTMLBody)
		if pdfErr != nil {
			log.Printf("Warning: Failed to convert HTML to PDF: %v", pdfErr)
			// Do not stop processing, PDF is optional
		} else {
			data.PDFBody = pdfBytes
			log.Printf("Successfully converted HTML to PDF, stored %d bytes.", len(data.PDFBody))
		}
	}

	return data, nil
}
