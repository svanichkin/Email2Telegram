package main

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/jhillyerd/enmime"

	xdraw "golang.org/x/image/draw"
)

// ParsedEmailData holds extracted information from an email
type ParsedEmailData struct {
	From            string
	To              string
	Subject         string
	UnsubscribeLink string
	TextBody        string // Только plain text
	HTMLBody        string // Только HTML
	HasText         bool   // Явное указание наличия текста
	HasHTML         bool   // Явное указание наличия HTML
	PDFBody         []byte
	PDFName         string
	PDFPreview      []byte
	Attachments     map[string][]byte
}

func ParseEmail(raw []byte) (*ParsedEmailData, error) {
	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}

	data := &ParsedEmailData{
		From:            env.GetHeader("From"),
		To:              env.GetHeader("To"),
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
		if pdf, preview, err := ConvertHTMLToPDF(data.HTMLBody); err == nil {
			data.PDFName = SanitizeFileName(data.Subject) + ".pdf"
			data.PDFBody = pdf
			data.PDFPreview = preview
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

func ConvertHTMLToPDF(htmlContent string) (pdfBytes []byte, previewJpeg []byte, err error) {
	if htmlContent == "" {
		err = fmt.Errorf("HTML content is empty")
		return
	}

	// Автоматический запуск браузера
	l := launcher.New().
		Headless(true).
		NoSandbox(true)

	url, lerr := l.Launch()
	if lerr != nil {
		err = fmt.Errorf("failed to launch browser: %w", lerr)
		return
	}

	browser := rod.New().ControlURL(url).MustConnect()
	defer browser.MustClose()

	page, perr := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if perr != nil {
		err = fmt.Errorf("failed to create page: %w", perr)
		return
	}
	defer page.MustClose()

	// Устанавливаем контент страницы
	if serr := page.SetDocumentContent(htmlContent); serr != nil {
		err = fmt.Errorf("failed to set content: %w", serr)
		return
	}

	// 1. Ожидаем полной загрузки страницы
	page.WaitLoad()

	// Ждем загрузки всех изображений
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
	time.Sleep(10 * time.Second)

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
	pdfStream, perr := page.PDF(pdfOpts)
	if perr != nil {
		err = fmt.Errorf("PDF generation failed: %w", perr)
		return
	}

	pdfBytes, perr = io.ReadAll(pdfStream)
	if perr != nil {
		err = fmt.Errorf("failed to read PDF stream: %w", perr)
		return
	}

	// Генерация превью JPEG (скриншот всей страницы)
	quality := 90
	screenshot, serr := page.Screenshot(false, &proto.PageCaptureScreenshot{
		Format:  proto.PageCaptureScreenshotFormatPng,
		Quality: &quality,
	})
	if serr != nil {
		err = fmt.Errorf("failed to capture screenshot: %w", serr)
		return
	}

	// Преобразуем PNG → image.Image
	img, _, derr := image.Decode(bytes.NewReader(screenshot))
	if derr != nil {
		err = fmt.Errorf("failed to decode screenshot: %w", derr)
		return
	}

	// Кроп в квадрат и ресайз
	square := cropToSquare(img)
	resized := resizeImage(square, 320, 320)

	var jpegBuf bytes.Buffer
	if jerr := jpeg.Encode(&jpegBuf, resized, &jpeg.Options{Quality: 80}); jerr != nil {
		err = fmt.Errorf("failed to encode JPEG: %w", jerr)
		return
	}

	if jpegBuf.Len() > 200*1024 {
		err = fmt.Errorf("preview too large: %d bytes", jpegBuf.Len())
		return
	}

	previewJpeg = jpegBuf.Bytes()
	return
}

func cropToSquare(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	size := w
	if h < w {
		size = h
	}
	// crop сверху (offsetY = 0)
	square := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(square, square.Bounds(), src, image.Pt(b.Min.X, b.Min.Y), draw.Src)
	return square
}

func resizeImage(img image.Image, width, height int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
	return dst
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
