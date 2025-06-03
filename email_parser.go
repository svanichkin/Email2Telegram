package main

import (
	"fmt"
	"regexp"
	"strings"

	goimap "github.com/BrianLeishman/go-imap"
	"github.com/PuerkitoBio/goquery"
	"github.com/microcosm-cc/bluemonday"
)

type ParsedEmailData struct {
	Uid         int
	From        string
	To          string
	Subject     string
	TextBody    string
	Attachments map[string][]byte
}

func ParseEmail(mail *goimap.Email, uid int) *ParsedEmailData {

	// Compile fields

	data := &ParsedEmailData{
		Uid: uid,

		From: parseAddressList(mail.From),
		To:   parseAddressList(mail.To),

		Subject:     mail.Subject,
		TextBody:    mail.Text,
		Attachments: make(map[string][]byte),
	}

	if len(mail.Attachments) > 0 {
		for _, a := range mail.Attachments {
			data.Attachments[a.Name] = a.Content
		}
	}

	if mail.HTML != "" {
		data.TextBody = CleanTelegramHTML(mail.HTML)
	}

	return data
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

	// Sanitize tags

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

func parseAddressList(m map[string]string) string {

	var result []string
	for address, name := range m {
		if name != "" {
			result = append(result, fmt.Sprintf("%s %s", address, name))
		} else {
			result = append(result, address)
		}
	}

	return strings.Join(result, ", ")
}
