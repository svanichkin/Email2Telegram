package main

import (
	"bytes"
	"fmt"
	"log"
	"net/mail"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/microcosm-cc/bluemonday"

	"github.com/jhillyerd/enmime"
)

// ParsedEmailData holds extracted information from an email
type ParsedEmailData struct {
	From        string
	To          string
	Subject     string
	TextBody    string
	Attachments map[string][]byte
}

func ParseEmail(raw []byte) (*ParsedEmailData, error) {

	// Eml reader

	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}

	// Compile fields

	data := &ParsedEmailData{
		Subject:     env.GetHeader("Subject"),
		TextBody:    env.Text,
		Attachments: make(map[string][]byte),
	}

	if from := env.GetHeader("From"); from != "" {
		data.From = parseAddressList(from)
	}
	if to := env.GetHeader("To"); to != "" {
		data.To = parseAddressList(to)
	} else {
		data.To = ""
	}
	if env.HTML != "" {
		data.TextBody = CleanTelegramHTML(env.HTML)
	}

	// Attachments

	for _, att := range env.Attachments {
		data.Attachments[att.FileName] = att.Content
	}

	return data, nil
}

func CleanTelegramHTML(raw string) string {

	raw = sanitizeHTML(raw)
	raw = strings.ToValidUTF8(raw, "")
	raw = strings.NewReplacer(
		"⠀", " ",
		"　", " ",
		" ", " ",
		" ", " ",
		"\u200c", " ",
		"\u00a0͏", " ",
		"\u00a0", " ",
		"\u034f", " ",
		"\t", "",
		"\r", "",
		"<br>", "\n",
		"<br />", "\n",
		"<br/>", "\n",
		"<p>", "\n",
		"</p>", "\n",
		"<strong>", "<b>",
		"</strong>", "</b>",
		"<em>", "<i>",
		"</em>", "</i>",
		"<strike>", "<s>",
		"</strike>", "</s>",
		"<del>", "<s>",
		"</del>", "</s>",
	).Replace(raw)
	raw = regexp.MustCompile(` {3,}`).ReplaceAllString(raw, "\n")
	raw = strings.NewReplacer(
		"\n ", " ",
		" \n", "\n",
	).Replace(raw)
	reNewlines := regexp.MustCompile(`(\n[\s]*){2,}`)
	for {
		old := raw
		raw = reNewlines.ReplaceAllString(raw, "\n")
		if raw == old {
			break
		}
	}
	raw = regexp.MustCompile(`\n.{1}\n`).ReplaceAllString(raw, "\n")
	for {
		old := raw
		raw = reNewlines.ReplaceAllString(raw, "\n")
		if raw == old {
			break
		}
	}
	raw = strings.NewReplacer(
		"\n<a href", "\n\n<a href",
		"</a>\n", "</a>\n\n",
		"\n<b", "\n\n<b",
		"</b>\n", "</b>\n\n",
	).Replace(raw)
	raw = regexp.MustCompile(`\n +`).ReplaceAllString(raw, "\n")
	raw = strings.TrimLeft(raw, "\n")
	raw = strings.TrimRight(raw, "\n")
	raw = strings.TrimLeft(raw, " ")
	raw = strings.TrimRight(raw, " ")
	fmt.Print(raw)

	return raw
}

func sanitizeHTML(html string) string {

	p := bluemonday.NewPolicy()
	p.AllowElements("b", "strong", "i", "em", "u", "s", "strike", "del", "a", "code", "pre", "p", "br")
	p.AllowAttrs("href").OnElements("a")
	html = p.Sanitize(html)

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return html
	}

	// Empty a href remove

	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		trimmed := strings.TrimSpace(s.Text())
		if trimmed == "" && len(s.Children().Nodes) == 0 {
			s.Remove()
		} else {
			s.SetText(trimmed)
		}
	})

	// Create html

	html, err = doc.Html()
	if err != nil {
		return html
	}

	// Clean headers

	html = strings.TrimPrefix(html, "<html><head></head><body>")
	html = strings.TrimSuffix(html, "</body></html>")

	return html
}

func parseAddressList(header string) string {

	addrs, err := mail.ParseAddressList(header)
	if err != nil {
		log.Fatal(err)
	}

	var result []string
	for _, addr := range addrs {
		if addr.Name != "" {
			result = append(result, fmt.Sprintf("%s %s", addr.Address, addr.Name))
		} else {
			result = append(result, addr.Address)
		}
	}

	return strings.Join(result, ", ")
}
