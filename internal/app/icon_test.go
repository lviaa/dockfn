package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSaveIconURIImportsAbsoluteFileURI(t *testing.T) {
	data := t.TempDir()
	canvas := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			canvas.Set(x, y, color.RGBA{20, 40, 60, 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(data, "source.png")
	if err := os.WriteFile(source, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	uri := "file:///" + strings.TrimPrefix(filepath.ToSlash(source), "/")
	path, err := saveIconURI(data, uri, "http", 80)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(data, filepath.FromSlash(path))); err != nil {
		t.Fatalf("imported icon was absent: %v", err)
	}
}

func TestSaveIconURIRelativePathUsesTargetService(t *testing.T) {
	icon := testICO(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/favicon.ico" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "image/vnd.microsoft.icon")
		_, _ = writer.Write(icon)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}

	data := t.TempDir()
	path, err := saveIconURI(data, "/favicon.ico", "http", uint16(port))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(data, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = png.Decode(bytes.NewReader(body)); err != nil {
		t.Fatalf("stored icon is not PNG: %v", err)
	}
}

func TestPreviewIconURIReturnsBrowserSafeDataURL(t *testing.T) {
	icon := testICO(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "image/vnd.microsoft.icon")
		_, _ = writer.Write(icon)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	dataURL, err := (&Service{}).PreviewIcon(context.Background(), IconPreviewInput{
		IconURI: "/favicon.ico", Protocol: "http", Port: uint16(port),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("unexpected preview %q", dataURL)
	}
}

func TestDiscoverIconUsesConfiguredPagePath(t *testing.T) {
	icon := testICO(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/":
			http.NotFound(writer, request)
		case "/panel/":
			_, _ = writer.Write([]byte(`<html><head><link rel="icon" href="/panel/icon.ico"></head></html>`))
		case "/panel/icon.ico":
			writer.Header().Set("Content-Type", "image/vnd.microsoft.icon")
			_, _ = writer.Write(icon)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&Service{}).DiscoverIcon(context.Background(), IconDiscoverInput{
		Protocol: "http", Port: uint16(port), Path: "/panel/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IconURI != "/panel/icon.ico" ||
		!strings.HasPrefix(result.DataURL, "data:image/png;base64,") {
		t.Fatalf("unexpected discovery result %#v", result)
	}
}

func TestDiscoverIconTriesCommonIconsRelativeToConfiguredPath(t *testing.T) {
	icon := testICO(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/panel":
			_, _ = writer.Write([]byte(`<html><head><title>Panel</title></head></html>`))
		case "/panel/favicon.ico":
			writer.Header().Set("Content-Type", "image/vnd.microsoft.icon")
			_, _ = writer.Write(icon)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&Service{}).DiscoverIcon(context.Background(), IconDiscoverInput{
		Protocol: "http", Port: uint16(port), Path: "/panel",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IconURI != "/panel/favicon.ico" {
		t.Fatalf("path-relative common icon was not discovered: %#v", result)
	}
}

func TestLinkedPageIconAcceptsShortcutIcon(t *testing.T) {
	page, err := url.Parse("http://127.0.0.1:12212/lvia")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`<link rel="shortcut icon" type="image/x-icon" href="/public/favicon.png">`)
	if got := linkedPageIcon(page, body); got != "/public/favicon.png" {
		t.Fatalf("shortcut icon was not parsed: %q", got)
	}
}

func TestDiscoverIconUsesPublicFaviconFallback(t *testing.T) {
	icon := testICO(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/lvia":
			_, _ = writer.Write([]byte(`<html><head><title>1Panel</title></head></html>`))
		case "/public/favicon.png":
			writer.Header().Set("Content-Type", "image/png")
			_, _ = writer.Write(icon)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&Service{}).DiscoverIcon(context.Background(), IconDiscoverInput{
		Protocol: "http", Port: uint16(port), Path: "/lvia",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IconURI != "/public/favicon.png" {
		t.Fatalf("public favicon fallback was not discovered: %#v", result)
	}
}

func TestDiscoverIconTriesLogoImageExtensions(t *testing.T) {
	icon := testICO(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/logo.jpg" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "image/vnd.microsoft.icon")
		_, _ = writer.Write(icon)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&Service{}).DiscoverIcon(context.Background(), IconDiscoverInput{
		Protocol: "http", Port: uint16(port), Path: "/missing",
	})
	if err != nil || result.IconURI != "/logo.jpg" {
		t.Fatalf("logo extension fallback was not discovered: result=%#v err=%v", result, err)
	}
}

func TestDiscoverIconReportsSVGWhenNoRasterIconIsAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/logo.svg" {
			writer.Header().Set("Content-Type", "image/svg+xml")
			_, _ = writer.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`))
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Service{}).DiscoverIcon(context.Background(), IconDiscoverInput{
		Protocol: "http", Port: uint16(port), Path: "/missing",
	})
	if err == nil || !strings.Contains(err.Error(), "SVG") {
		t.Fatalf("SVG-only discovery did not provide an actionable error: %v", err)
	}
}

func TestLinkedPageIconRejectsCrossOriginReference(t *testing.T) {
	page, err := url.Parse("http://127.0.0.1:8080/panel/")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`<link rel="icon" href="http://example.test/tracker.png">`)
	if got := linkedPageIcon(page, body); got != "" {
		t.Fatalf("cross-origin page icon was accepted: %q", got)
	}
}

func TestSaveIconBytesAcceptsClassic32BitICO(t *testing.T) {
	data := t.TempDir()
	path, err := saveIconBytes(data, testICO(t))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(data, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("stored icon is not PNG: %v", err)
	}
	got := color.RGBAModel.Convert(decoded.At(32, 32)).(color.RGBA)
	if got.R < 200 || got.A != 255 {
		t.Fatalf("ICO pixel was not decoded: %#v", got)
	}
}

func TestSaveIconBytesResizesLargeRasterIcon(t *testing.T) {
	data := t.TempDir()
	canvas := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			canvas.Set(x, y, color.RGBA{uint8(x % 251), uint8(y % 251), 80, 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	path, err := saveIconBytes(data, encoded.Bytes())
	if err != nil {
		t.Fatalf("large raster icon should be normalized automatically: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(data, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != 64 || decoded.Bounds().Dy() != 64 {
		t.Fatalf("large raster icon was not normalized to 64x64: %v", decoded.Bounds())
	}
}

func TestSaveIconAcceptsICOWithGenericBrowserMIMEType(t *testing.T) {
	data := t.TempDir()
	encoded := "data:application/octet-stream;base64," + base64.StdEncoding.EncodeToString(testICO(t))
	if _, err := saveIcon(data, encoded); err != nil {
		t.Fatalf("generic MIME type prevented content-validated ICO import: %v", err)
	}
}

func TestParseIconURIRejectsCredentialsAndSchemeRelativeHosts(t *testing.T) {
	for _, value := range []string{
		"http://user:secret@127.0.0.1/favicon.ico",
		"//example.test/favicon.ico",
	} {
		if _, err := parseIconURI(value, "http", 8080); err == nil {
			t.Fatalf("unsafe icon URI %q was accepted", value)
		}
	}
}

func TestResolveIconRejectsCompetingSources(t *testing.T) {
	service := &Service{DataDir: t.TempDir()}
	encoded := "aGVsbG8="
	uri := "https://example.test/icon.png"
	if _, _, err := service.resolveIcon(Input{IconBase64: &encoded, IconURI: &uri}, ""); err == nil {
		t.Fatal("expected competing sources to be rejected")
	}
}

func testICO(t *testing.T) []byte {
	t.Helper()
	const (
		width       = 2
		height      = 2
		headerBytes = 40
		pixelBytes  = width * height * 4
		maskBytes   = 4 * height
		imageBytes  = headerBytes + pixelBytes + maskBytes
	)
	body := make([]byte, 6+16+imageBytes)
	binary.LittleEndian.PutUint16(body[0:2], 0)
	binary.LittleEndian.PutUint16(body[2:4], 1)
	binary.LittleEndian.PutUint16(body[4:6], 1)
	body[6], body[7] = width, height
	binary.LittleEndian.PutUint16(body[10:12], 1)
	binary.LittleEndian.PutUint16(body[12:14], 32)
	binary.LittleEndian.PutUint32(body[14:18], imageBytes)
	binary.LittleEndian.PutUint32(body[18:22], 22)
	imageBody := body[22:]
	binary.LittleEndian.PutUint32(imageBody[0:4], headerBytes)
	binary.LittleEndian.PutUint32(imageBody[4:8], width)
	binary.LittleEndian.PutUint32(imageBody[8:12], height*2)
	binary.LittleEndian.PutUint16(imageBody[12:14], 1)
	binary.LittleEndian.PutUint16(imageBody[14:16], 32)
	binary.LittleEndian.PutUint32(imageBody[20:24], pixelBytes)
	for offset := headerBytes; offset < headerBytes+pixelBytes; offset += 4 {
		imageBody[offset+0] = 10
		imageBody[offset+1] = 20
		imageBody[offset+2] = 240
		imageBody[offset+3] = 255
	}
	return body
}
