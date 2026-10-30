package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"

	// 注册解码器：按需保留用到的格式
	_ "image/gif"
	_ "image/png"
)

// ImageCompressEngine 图片压缩引擎示例（标准库实现：解码 → 按质量重编码 JPEG）。
// 需要等比缩放时引入 golang.org/x/image/draw，在本文件内完成，不影响其他引擎。
type ImageCompressEngine struct{}

func (ImageCompressEngine) Name() string { return "image-compress" }

func (ImageCompressEngine) Description() string {
	return "压缩 base64 编码的图片，返回压缩后的 base64 与体积对比"
}

type imageCompressPayload struct {
	DataBase64 string `json:"data_base64"`
	Quality    int    `json:"quality"`
}

type imageCompressOutput struct {
	DataBase64 string `json:"data_base64"`
	BeforeSize int    `json:"before_bytes"`
	AfterSize  int    `json:"after_bytes"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

func (ImageCompressEngine) Execute(ctx context.Context, payload []byte) (any, error) {
	var p imageCompressPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("%w: malformed payload: %v", ErrInvalidInput, err)
	}
	raw, err := base64.StdEncoding.DecodeString(p.DataBase64)
	if err != nil {
		return nil, fmt.Errorf("%w: payload.data_base64 is not valid base64", ErrInvalidInput)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: payload.data_base64 is empty", ErrInvalidInput)
	}
	if p.Quality < 1 || p.Quality > 100 {
		p.Quality = 70
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: unsupported or corrupted image", ErrInvalidInput)
	}

	// CPU 密集操作：耗时前后检查取消信号，保证超时/客户端断开能尽早返回
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: p.Quality}); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	b := img.Bounds()
	return imageCompressOutput{
		DataBase64: base64.StdEncoding.EncodeToString(buf.Bytes()),
		BeforeSize: len(raw),
		AfterSize:  buf.Len(),
		Width:      b.Dx(),
		Height:     b.Dy(),
	}, nil
}
