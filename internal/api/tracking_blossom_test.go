package api

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

func TestObjectMediaCanCreateAnimatedGIF(t *testing.T) {
	dir := t.TempDir()
	crops := filepath.Join(dir, "event", "track_crops", "object_0001")
	if err := os.MkdirAll(crops, 0750); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		img := image.NewRGBA(image.Rect(0, 0, 16, 8))
		img.Set(0, 0, color.RGBA{R: uint8(50 + i*100), A: 255})
		var data bytes.Buffer
		if err := jpeg.Encode(&data, img, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(crops, fmt.Sprintf("frame_%06d_%09dms.jpg", i+1, i*33)), data.Bytes(), 0640); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data, mime, err := objectMedia(root, "event", 1, true)
	if err != nil || mime != "image/gif" {
		t.Fatalf("GIF: %s %v", mime, err)
	}
	animation, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil || len(animation.Image) != 2 {
		t.Fatalf("GIF frames: %v %v", animation, err)
	}
}

func TestRealBlossomUpload(t *testing.T) {
	serverURL := os.Getenv("TRACKFLIGHTS_TEST_BLOSSOM")
	if serverURL == "" {
		t.Skip("set TRACKFLIGHTS_TEST_BLOSSOM to an isolated test server")
	}
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	img.Set(1, 1, color.RGBA{R: 210, G: 34, A: 255})
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	media, tag, err := uploadBlossomMedia(ctx, serverURL, "", nostr.GeneratePrivateKey(), encoded.Bytes(), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if len(tag) < 4 {
		t.Fatalf("invalid media tag: %v", tag)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, media.URL, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || !bytes.Equal(data, encoded.Bytes()) {
		t.Fatalf("download: status=%d error=%v", response.StatusCode, err)
	}
}
