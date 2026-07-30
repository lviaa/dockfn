package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dockfn/dockfn/internal/iconimage"
)

const maxIconBytes = 512 << 10

var (
	iconLinkPattern      = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	iconAttributePattern = regexp.MustCompile("(?is)([a-z_:][-a-z0-9_:.]*)\\s*=\\s*(?:\\\"([^\\\"]*)\\\"|'([^']*)'|([^\\s>]+))")
	commonIconURIs       = []string{
		"/favicon.ico",
		"/favicon.png",
		"/apple-touch-icon.png",
		"/apple-touch-icon-precomposed.png",
		"/icon.png",
		"/public/favicon.png",
	}
)

type IconPreviewInput struct {
	IconURI  string `json:"iconUri"`
	Protocol string `json:"protocol"`
	Port     uint16 `json:"port"`
}

type IconDiscoverInput struct {
	Protocol string `json:"protocol"`
	Port     uint16 `json:"port"`
	Path     string `json:"path"`
}

type IconDiscovery struct {
	IconURI string `json:"iconUri"`
	DataURL string `json:"dataUrl"`
}

func saveIcon(dataDir, encoded string) (string, error) {
	encoded = strings.TrimSpace(encoded)
	if comma := strings.IndexByte(encoded, ','); strings.HasPrefix(encoded, "data:") && comma >= 0 {
		header := encoded[:comma]
		mediaType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		if !strings.HasSuffix(header, ";base64") ||
			(mediaType != "image/png" && mediaType != "image/jpeg" &&
				mediaType != "image/x-icon" && mediaType != "image/vnd.microsoft.icon" &&
				mediaType != "application/octet-stream" && mediaType != "") {
			return "", errors.New("icon must be a PNG, JPEG, or ICO data URL")
		}
		encoded = encoded[comma+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.New("icon is not valid base64")
	}
	return saveIconBytes(dataDir, raw)
}

func saveIconURI(dataDir, value, protocol string, port uint16) (string, error) {
	raw, err := readIconURI(context.Background(), value, protocol, port)
	if err != nil {
		return "", err
	}
	return saveIconBytes(dataDir, raw)
}

func readIconURI(ctx context.Context, value, protocol string, port uint16) ([]byte, error) {
	parsed, err := parseIconURI(value, protocol, port)
	if err != nil {
		return nil, err
	}
	var raw []byte
	switch parsed.Scheme {
	case "file":
		if parsed.Host != "" && parsed.Host != "localhost" {
			return nil, errors.New("icon file URI must not specify a remote host")
		}
		decodedPath, decodeErr := url.PathUnescape(parsed.Path)
		if decodeErr != nil {
			return nil, errors.New("icon file URI has an invalid path")
		}
		path := filepath.FromSlash(decodedPath)
		if runtime.GOOS == "windows" && len(path) > 3 && path[0] == '\\' && path[2] == ':' {
			path = path[1:]
		}
		if !filepath.IsAbs(path) {
			return nil, errors.New("icon file URI must be absolute")
		}
		raw, err = os.ReadFile(path)
	case "http", "https":
		raw, err = downloadIconContext(ctx, parsed.String())
	default:
		return nil, errors.New("icon URI scheme must be file, http, or https")
	}
	if err != nil {
		return nil, fmt.Errorf("read icon URI: %w", err)
	}
	return raw, nil
}

func parseIconURI(value, protocol string, port uint16) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("icon URI must not be empty")
	}
	if strings.HasPrefix(value, "//") {
		return nil, errors.New("icon URI must not use a scheme-relative host")
	}
	if !strings.Contains(value, "://") && !strings.HasPrefix(value, "file:") {
		if protocol != "http" && protocol != "https" {
			return nil, errors.New("relative icon URI requires an HTTP or HTTPS service")
		}
		firstSegment := strings.SplitN(strings.TrimPrefix(value, "//"), "/", 2)[0]
		if strings.Contains(firstSegment, ":") {
			value = protocol + "://" + strings.TrimPrefix(value, "//")
		} else {
			base := &url.URL{
				Scheme: protocol,
				Host:   "127.0.0.1:" + strconv.Itoa(int(port)),
				Path:   "/",
			}
			reference, err := url.Parse(value)
			if err != nil {
				return nil, errors.New("icon URI is invalid")
			}
			value = base.ResolveReference(reference).String()
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return nil, errors.New("icon URI must be a relative service path or a file, HTTP, or HTTPS URI")
	}
	if parsed.User != nil {
		return nil, errors.New("icon URI must not contain credentials")
	}
	return parsed, nil
}

func downloadIconContext(ctx context.Context, uri string) ([]byte, error) {
	redirects := 0
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			redirects++
			if redirects > 3 || request.URL.Scheme != "http" && request.URL.Scheme != "https" ||
				request.URL.User != nil || len(via) > 0 && request.URL.Scheme != via[0].URL.Scheme {
				return errors.New("icon URI redirect was rejected")
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("icon URI returned HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, maxIconBytes+1))
}

func saveIconBytes(dataDir string, raw []byte) (string, error) {
	source, err := decodeIconSource(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	id := hex.EncodeToString(sum[:16])
	relative := filepath.ToSlash(filepath.Join("icons", id, "ICON.PNG"))
	root := filepath.Join(dataDir, "icons", id)
	if err = os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	for _, size := range []int{64, 256} {
		name := "ICON.PNG"
		if size == 256 {
			name = "ICON_256.PNG"
		}
		body, resizeErr := resizePNG(source, size)
		if resizeErr != nil {
			return "", resizeErr
		}
		if err = atomicWrite(filepath.Join(root, name), body, 0o600); err != nil {
			return "", err
		}
	}
	return relative, nil
}

func decodeIconSource(raw []byte) (image.Image, error) {
	if len(raw) == 0 || len(raw) > maxIconBytes {
		return nil, errors.New("icon must be between 1 byte and 512 KiB")
	}
	source, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil || (format != "png" && format != "jpeg") {
		source, err = decodeICO(raw)
		if err != nil {
			return nil, errors.New("icon must contain a valid PNG, JPEG, or ICO image")
		}
	}
	bounds := source.Bounds()
	if bounds.Dx() < 1 || bounds.Dy() < 1 || bounds.Dx() > 2048 || bounds.Dy() > 2048 {
		return nil, errors.New("icon dimensions must be between 1x1 and 2048x2048")
	}
	return source, nil
}

func (s *Service) PreviewIcon(ctx context.Context, input IconPreviewInput) (string, error) {
	raw, err := readIconURI(
		ctx,
		strings.TrimSpace(input.IconURI),
		strings.ToLower(strings.TrimSpace(input.Protocol)),
		input.Port,
	)
	if err != nil {
		return "", iconValidationError("iconUri", err)
	}
	source, err := decodeIconSource(raw)
	if err != nil {
		return "", iconValidationError("iconUri", err)
	}
	body, err := resizePNG(source, 64)
	if err != nil {
		return "", iconValidationError("iconUri", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(body), nil
}

func (s *Service) DiscoverIcon(ctx context.Context, input IconDiscoverInput) (IconDiscovery, error) {
	protocol := strings.ToLower(strings.TrimSpace(input.Protocol))
	if protocol != "http" && protocol != "https" {
		return IconDiscovery{}, iconValidationError("protocol", errors.New("must be http or https"))
	}
	if input.Port == 0 {
		return IconDiscovery{}, iconValidationError("port", errors.New("must be between 1 and 65535"))
	}
	if err := validatePath(input.Path); err != nil {
		return IconDiscovery{}, iconValidationError("path", err)
	}
	candidates := make([]string, 0, len(commonIconURIs)+1)
	if pageURL, body, err := readServicePage(ctx, protocol, input.Port, input.Path); err == nil {
		if linked := linkedPageIcon(pageURL, body); linked != "" {
			candidates = append(candidates, linked)
		}
	}
	if basePath := strings.TrimSuffix(input.Path, "/"); basePath != "" {
		for _, iconURI := range commonIconURIs {
			candidates = append(candidates, basePath+iconURI)
		}
	}
	candidates = append(candidates, commonIconURIs...)
	seen := make(map[string]bool, len(candidates))
	for _, iconURI := range candidates {
		if seen[iconURI] {
			continue
		}
		seen[iconURI] = true
		dataURL, err := s.PreviewIcon(ctx, IconPreviewInput{
			IconURI: iconURI, Protocol: protocol, Port: input.Port,
		})
		if err == nil {
			return IconDiscovery{IconURI: iconURI, DataURL: dataURL}, nil
		}
	}
	return IconDiscovery{}, iconValidationError("iconUri", errors.New("no page or common service icon was found"))
}

func readServicePage(ctx context.Context, protocol string, port uint16, path string) (*url.URL, []byte, error) {
	target, err := url.Parse(protocol + "://" +
		net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))) + path)
	if err != nil {
		return nil, nil, err
	}
	transport := &http.Transport{DisableKeepAlives: true}
	if protocol == "https" {
		transport.TLSClientConfig = localTLSConfig()
	}
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 || request.URL.Scheme != target.Scheme ||
				!strings.EqualFold(request.URL.Host, target.Host) || request.URL.User != nil {
				return errors.New("page redirect was rejected")
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Range", "bytes=0-65535")
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, nil, fmt.Errorf("page returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > 64<<10 {
		return nil, nil, errors.New("page metadata exceeds 64 KiB")
	}
	return response.Request.URL, body, nil
}

func localTLSConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- loopback metadata discovery only
}

func linkedPageIcon(pageURL *url.URL, body []byte) string {
	for _, tag := range iconLinkPattern.FindAll(body, -1) {
		attributes := parseIconAttributes(string(tag))
		if !isIconRelationship(attributes["rel"]) {
			continue
		}
		reference, err := url.Parse(html.UnescapeString(attributes["href"]))
		if err != nil || reference.User != nil {
			continue
		}
		resolved := pageURL.ResolveReference(reference)
		if resolved.Scheme != pageURL.Scheme || !strings.EqualFold(resolved.Host, pageURL.Host) ||
			resolved.Path == "" {
			continue
		}
		result := resolved.EscapedPath()
		if resolved.RawQuery != "" {
			result += "?" + resolved.RawQuery
		}
		return result
	}
	return ""
}

func parseIconAttributes(tag string) map[string]string {
	attributes := make(map[string]string)
	for _, match := range iconAttributePattern.FindAllStringSubmatch(tag, -1) {
		for index := 2; index < len(match); index++ {
			if match[index] != "" {
				attributes[strings.ToLower(match[1])] = strings.TrimSpace(match[index])
				break
			}
		}
	}
	return attributes
}

func isIconRelationship(value string) bool {
	for _, relationship := range strings.Fields(strings.ToLower(value)) {
		if relationship == "icon" || relationship == "shortcut" ||
			relationship == "apple-touch-icon" || relationship == "apple-touch-icon-precomposed" {
			return true
		}
	}
	return false
}

func decodeICO(raw []byte) (image.Image, error) {
	if len(raw) < 22 || binary.LittleEndian.Uint16(raw[0:2]) != 0 ||
		binary.LittleEndian.Uint16(raw[2:4]) != 1 {
		return nil, errors.New("invalid ICO header")
	}
	count := int(binary.LittleEndian.Uint16(raw[4:6]))
	if count < 1 || len(raw) < 6+count*16 {
		return nil, errors.New("invalid ICO directory")
	}
	best := -1
	bestScore := -1
	for index := 0; index < count; index++ {
		entry := raw[6+index*16 : 6+(index+1)*16]
		width, height := int(entry[0]), int(entry[1])
		if width == 0 {
			width = 256
		}
		if height == 0 {
			height = 256
		}
		size := int(binary.LittleEndian.Uint32(entry[8:12]))
		offset := int(binary.LittleEndian.Uint32(entry[12:16]))
		if width > 2048 || height > 2048 || size < 1 || offset < 0 || offset > len(raw)-size {
			continue
		}
		score := width*height*100 + int(binary.LittleEndian.Uint16(entry[6:8]))
		if score > bestScore {
			best, bestScore = index, score
		}
	}
	if best < 0 {
		return nil, errors.New("ICO contains no usable image")
	}
	entry := raw[6+best*16 : 6+(best+1)*16]
	width, height := int(entry[0]), int(entry[1])
	if width == 0 {
		width = 256
	}
	if height == 0 {
		height = 256
	}
	size := int(binary.LittleEndian.Uint32(entry[8:12]))
	offset := int(binary.LittleEndian.Uint32(entry[12:16]))
	payload := raw[offset : offset+size]
	if bytes.HasPrefix(payload, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return png.Decode(bytes.NewReader(payload))
	}
	return decodeICODIB(payload, width, height)
}

func decodeICODIB(payload []byte, directoryWidth, directoryHeight int) (image.Image, error) {
	if len(payload) < 40 {
		return nil, errors.New("ICO bitmap header is truncated")
	}
	headerSize := int(binary.LittleEndian.Uint32(payload[0:4]))
	bitmapWidth := int(int32(binary.LittleEndian.Uint32(payload[4:8])))
	bitmapHeight := int(int32(binary.LittleEndian.Uint32(payload[8:12])))
	bitCount := int(binary.LittleEndian.Uint16(payload[14:16]))
	compression := binary.LittleEndian.Uint32(payload[16:20])
	if headerSize < 40 || headerSize > len(payload) || bitmapWidth == 0 || bitmapHeight == 0 ||
		(bitCount != 24 && bitCount != 32) || compression != 0 {
		return nil, errors.New("ICO bitmap format is unsupported")
	}
	topDown := bitmapHeight < 0
	if bitmapWidth < 0 {
		bitmapWidth = -bitmapWidth
	}
	if bitmapHeight < 0 {
		bitmapHeight = -bitmapHeight
	}
	bitmapHeight /= 2
	if bitmapWidth != directoryWidth || bitmapHeight != directoryHeight ||
		bitmapWidth < 1 || bitmapHeight < 1 || bitmapWidth > 2048 || bitmapHeight > 2048 {
		return nil, errors.New("ICO bitmap dimensions are invalid")
	}
	colorRowBytes := ((bitmapWidth*bitCount + 31) / 32) * 4
	colorBytes := colorRowBytes * bitmapHeight
	if colorRowBytes <= 0 || headerSize > len(payload)-colorBytes {
		return nil, errors.New("ICO bitmap pixels are truncated")
	}
	maskRowBytes := ((bitmapWidth + 31) / 32) * 4
	maskOffset := headerSize + colorBytes
	hasMask := maskRowBytes > 0 && maskOffset <= len(payload)-maskRowBytes*bitmapHeight
	result := image.NewNRGBA(image.Rect(0, 0, bitmapWidth, bitmapHeight))
	hasAlpha := false
	for y := 0; y < bitmapHeight; y++ {
		sourceY := bitmapHeight - 1 - y
		if topDown {
			sourceY = y
		}
		row := payload[headerSize+sourceY*colorRowBytes:]
		for x := 0; x < bitmapWidth; x++ {
			pixelOffset := x * (bitCount / 8)
			alpha := uint8(255)
			if bitCount == 32 {
				alpha = row[pixelOffset+3]
				hasAlpha = hasAlpha || alpha != 0
			}
			result.SetNRGBA(x, y, color.NRGBA{
				R: row[pixelOffset+2],
				G: row[pixelOffset+1],
				B: row[pixelOffset],
				A: alpha,
			})
		}
	}
	if bitCount == 24 || !hasAlpha {
		for y := 0; y < bitmapHeight; y++ {
			sourceY := bitmapHeight - 1 - y
			if topDown {
				sourceY = y
			}
			for x := 0; x < bitmapWidth; x++ {
				alpha := uint8(255)
				if hasMask {
					mask := payload[maskOffset+sourceY*maskRowBytes+x/8]
					if mask&(0x80>>uint(x%8)) != 0 {
						alpha = 0
					}
				}
				pixel := result.NRGBAAt(x, y)
				pixel.A = alpha
				result.SetNRGBA(x, y, pixel)
			}
		}
	}
	return result, nil
}

func resizePNG(source image.Image, size int) ([]byte, error) {
	destination := iconimage.FitSquare(source, size)
	var output bytes.Buffer
	if err := png.Encode(&output, destination); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func iconDataURL(dataDir, relative string) (string, error) {
	if relative == "" {
		return "", nil
	}
	path := filepath.Join(dataDir, filepath.FromSlash(relative))
	if !within(filepath.Join(dataDir, "icons"), path) {
		return "", errors.New("icon escaped icon directory")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(body) > maxIconBytes {
		return "", errors.New("stored icon exceeds limit")
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(body), nil
}
