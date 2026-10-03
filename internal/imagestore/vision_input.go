package imagestore

import (
	"bytes"
	"errors"
	"image"
	"image/png"
)

type boundedPNG struct{ bytes.Buffer }

func (b *boundedPNG) Write(p []byte) (int, error) {
	if b.Len()+len(p) > MaxBytes {
		return 0, errors.New("converted image exceeds size limit")
	}
	return b.Buffer.Write(p)
}

// VisionInput preserves stored original bytes; static formats are converted only
// for provider input. Animation is explicitly rejected, never reduced silently.
func VisionInput(body []byte, mediaType string) ([]byte, string, error) {
	if len(body) == 0 || len(body) > MaxBytes {
		return nil, "", errors.New("invalid vision image size")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 20_000_000 {
		return nil, "", errors.New("invalid vision image dimensions")
	}
	if format == "gif" {
		if err := staticGIF(body); err != nil {
			return nil, "", err
		}
	}
	decoded, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, "", errors.New("corrupt vision image")
	}
	if format == "jpeg" && mediaType == "image/jpeg" || format == "png" && mediaType == "image/png" {
		return body, mediaType, nil
	}
	if format != "gif" && format != "webp" {
		return nil, "", errors.New("unsupported vision format")
	}
	var output boundedPNG
	if err := png.Encode(&output, decoded); err != nil {
		return nil, "", err
	}
	return output.Bytes(), "image/png", nil
}

func staticGIF(body []byte) error {
	if len(body) < 13 {
		return errors.New("invalid GIF header")
	}
	position := 13
	if body[10]&0x80 != 0 {
		position += 3 * (1 << ((body[10] & 7) + 1))
	}
	frames := 0
	skipBlocks := func() bool {
		for position < len(body) {
			size := int(body[position])
			position++
			if size == 0 {
				return true
			}
			position += size
		}
		return false
	}
	for position < len(body) {
		marker := body[position]
		position++
		switch marker {
		case 0x3b:
			if frames == 1 {
				return nil
			}
			return errors.New("GIF has no frame")
		case 0x21:
			position++
			if !skipBlocks() {
				return errors.New("invalid GIF extension")
			}
		case 0x2c:
			frames++
			if frames > 1 {
				return errors.New("animated GIF requires explicit handling; cannot use a single frame")
			}
			if position+9 > len(body) {
				return errors.New("invalid GIF frame")
			}
			flags := body[position+8]
			position += 9
			if flags&0x80 != 0 {
				position += 3 * (1 << ((flags & 7) + 1))
			}
			position++
			if !skipBlocks() {
				return errors.New("invalid GIF data")
			}
		default:
			return errors.New("invalid GIF block")
		}
	}
	return errors.New("GIF trailer missing")
}
