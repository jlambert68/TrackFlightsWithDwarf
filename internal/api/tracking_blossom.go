package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

type blossomMedia struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	MIME   string `json:"mime"`
	Size   int    `json:"size"`
}

// objectMedia returns the old tracker's representative crop, or a GIF assembled
// from sampled crop frames when requested. All files are confined to root.
func objectMedia(root *os.Root, eventID string, trackID int, preferGIF bool) ([]byte, string, error) {
	dir := filepath.Join(eventID, "track_crops", fmt.Sprintf("object_%04d", trackID))
	read := func(name string) ([]byte, error) {
		f, err := root.Open(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
		if err != nil {
			return nil, err
		}
		if len(data) == 0 || len(data) > 8<<20 {
			return nil, fmt.Errorf("object media is empty or exceeds 8 MiB")
		}
		return data, nil
	}
	if preferGIF {
		if data, err := read("object.gif"); err == nil && http.DetectContentType(data) == "image/gif" {
			return data, "image/gif", nil
		}
	}
	entries, err := fs.ReadDir(root.FS(), dir)
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var crops []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && objectCropName.MatchString(entry.Name()) {
			crops = append(crops, entry.Name())
		}
	}
	sort.Strings(crops)
	if len(crops) == 0 {
		return nil, "", nil
	}
	if preferGIF && len(crops) > 1 {
		animation := gif.GIF{LoopCount: 0}
		count := min(len(crops), 80)
		for i := 0; i < count; i++ {
			data, err := read(crops[i*(len(crops)-1)/(count-1)])
			if err != nil {
				return nil, "", err
			}
			config, _, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 16_000_000 {
				return nil, "", fmt.Errorf("invalid object image dimensions")
			}
			img, _, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				return nil, "", err
			}
			bounds := img.Bounds()
			if bounds.Empty() || bounds.Dx() > 8192 || bounds.Dy() > 8192 {
				return nil, "", fmt.Errorf("invalid object image dimensions")
			}
			// A fixed canvas supports varying crop sizes and bounds GIF memory.
			frame := image.NewPaletted(image.Rect(0, 0, 256, 256), palette.Plan9)
			scaled := image.NewRGBA(frame.Rect)
			width, height := 256, 256
			if bounds.Dx() > bounds.Dy() {
				height = max(1, 256*bounds.Dy()/bounds.Dx())
			} else {
				width = max(1, 256*bounds.Dx()/bounds.Dy())
			}
			left, top := (256-width)/2, (256-height)/2
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					scaled.Set(left+x, top+y, img.At(bounds.Min.X+x*bounds.Dx()/width, bounds.Min.Y+y*bounds.Dy()/height))
				}
			}
			draw.FloydSteinberg.Draw(frame, frame.Rect, scaled, image.Point{})
			animation.Image = append(animation.Image, frame)
			animation.Delay = append(animation.Delay, 6)
		}
		var buffer bytes.Buffer
		if err := gif.EncodeAll(&buffer, &animation); err != nil {
			return nil, "", err
		}
		if buffer.Len() > 8<<20 {
			return nil, "", fmt.Errorf("object GIF exceeds 8 MiB")
		}
		return buffer.Bytes(), "image/gif", nil
	}
	index := 0
	if len(crops) >= 10 {
		index = 9
	} else if len(crops) >= 2 {
		index = len(crops) - 2
	}
	data, err := read(crops[index])
	if err != nil {
		return nil, "", err
	}
	if http.DetectContentType(data) != "image/jpeg" {
		return nil, "", fmt.Errorf("object crop is not a JPEG")
	}
	return data, "image/jpeg", nil
}

func uploadBlossomMedia(ctx context.Context, serverURL, publicURL, key string, data []byte, mimeType string) (*blossomMedia, nostr.Tag, error) {
	base, err := url.Parse(serverURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" {
		return nil, nil, fmt.Errorf("invalid Blossom URL")
	}
	hash := sha256.Sum256(data)
	hashText := hex.EncodeToString(hash[:])
	auth := nostr.Event{CreatedAt: nostr.Now(), Kind: 24242, Content: "Authorize upload", Tags: nostr.Tags{{"t", "upload"}, {"expiration", fmt.Sprint(time.Now().Unix() + 60)}, {"x", hashText}}}
	if err := auth.Sign(key); err != nil {
		return nil, nil, err
	}
	authJSON, err := json.Marshal(auth)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimRight(serverURL, "/")+"/upload", bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(authJSON))
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("X-SHA-256", hashText)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, nil, fmt.Errorf("Blossom upload returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var descriptor struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&descriptor); err != nil {
		return nil, nil, fmt.Errorf("invalid Blossom response: %w", err)
	}
	if descriptor.SHA256 != hashText {
		return nil, nil, fmt.Errorf("Blossom returned a different content hash")
	}
	if publicURL == "" {
		publicURL = serverURL
	}
	ext := ".jpg"
	if mimeType == "image/gif" {
		ext = ".gif"
	}
	media := &blossomMedia{URL: strings.TrimRight(publicURL, "/") + "/" + hashText + ext, SHA256: hashText, MIME: mimeType, Size: len(data)}
	tag := nostr.Tag{"imeta", "url " + media.URL, "m " + media.MIME, "x " + media.SHA256, fmt.Sprintf("size %d", media.Size)}
	return media, tag, nil
}
