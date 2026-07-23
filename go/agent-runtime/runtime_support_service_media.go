package agentruntime

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	_ "golang.org/x/image/webp"
	_ "image/gif" // 注册 GIF 解码器。
)

const (
	valueImageGifD35F50B8 = "image/gif"
)

// resizeImageIfNeeded 在图片尺寸超过 maxDim 时进行缩放并重新编码。
// 若解码/编码失败则返回原始字节，不报错，保证降级可用。
// 使用最近邻插值以降低 CPU 开销，缩略图语义信息仍足够供 LLM 识别。
func resizeImageIfNeeded(data []byte, mimeType string, maxDim int) []byte {
	if maxDim <= 0 || len(data) == 0 {
		return data
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data // 无法解码时返回原始数据，由上游模型按原图处理。
	}

	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= maxDim && h <= maxDim {
		return data
	}

	dst := resizeImageNearestNeighbor(src, bounds, mediaResizeScale(w, h, maxDim))
	resized, err := encodeResizedImage(dst, mimeType)
	if err != nil {
		return data
	}
	return resized
}

func mediaResizeScale(width int, height int, maxDim int) float64 {
	if width >= height {
		return float64(maxDim) / float64(width)
	}
	return float64(maxDim) / float64(height)
}

func resizeImageNearestNeighbor(src image.Image, bounds image.Rectangle, scale float64) *image.NRGBA {
	newW := max(1, int(math.Round(float64(bounds.Dx())*scale)))
	newH := max(1, int(math.Round(float64(bounds.Dy())*scale)))
	dst := image.NewNRGBA(image.Rect(0, 0, newW, newH))
	for dy := 0; dy < newH; dy++ {
		for dx := 0; dx < newW; dx++ {
			sx := clampImageCoordinate(int(float64(dx)/scale)+bounds.Min.X, bounds.Max.X)
			sy := clampImageCoordinate(int(float64(dy)/scale)+bounds.Min.Y, bounds.Max.Y)
			dst.Set(dx, dy, src.At(sx, sy))
		}
	}
	return dst
}

func clampImageCoordinate(value int, maxValue int) int {
	if value >= maxValue {
		return maxValue - 1
	}
	return value
}

func encodeResizedImage(imageData image.Image, mimeType string) ([]byte, error) {
	var buf bytes.Buffer
	if strings.Contains(strings.ToLower(strings.TrimSpace(mimeType)), "png") {
		if err := png.Encode(&buf, imageData); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	if err := jpeg.Encode(&buf, imageData, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// resolveImageMimeType 规范化图片 MIME 类型，未知时默认为 image/jpeg。
func resolveImageMimeType(mimeType string) string {
	normalized := strings.ToLower(strings.TrimSpace(mimeType))
	switch normalized {
	case "image/jpeg", "image/jpg", "image/png", valueImageGifD35F50B8, "image/webp":
		return normalized
	default:
		return "image/jpeg"
	}
}
