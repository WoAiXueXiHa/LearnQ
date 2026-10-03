package imagestore

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"testing"
)

func TestStaticGIFConvertedAndAnimationRejected(t *testing.T) {
	frame := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	frame.SetColorIndex(0, 0, 1)
	var static bytes.Buffer
	if err := gif.Encode(&static, frame, nil); err != nil {
		t.Fatal(err)
	}
	body, media, err := VisionInput(static.Bytes(), "image/gif")
	if err != nil || media != "image/png" {
		t.Fatalf("conversion: %s %v", media, err)
	}
	decoded, format, err := image.Decode(bytes.NewReader(body))
	if err != nil || format != "png" || decoded.Bounds() != frame.Bounds() {
		t.Fatalf("converted bounds/format: %s %v", format, err)
	}
	var animated bytes.Buffer
	if err := gif.EncodeAll(&animated, &gif.GIF{Image: []*image.Paletted{frame, frame}, Delay: []int{1, 1}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := VisionInput(animated.Bytes(), "image/gif"); err == nil {
		t.Fatal("animation silently reduced to one frame")
	}
	if _, _, err := VisionInput(static.Bytes()[:10], "image/gif"); err == nil {
		t.Fatal("truncated GIF accepted")
	}
}
