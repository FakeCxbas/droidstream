package v4l2

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

type FourCC uint32

const (
	V4L2_BUF_TYPE_VIDEO_OUTPUT  uint32 = 2
	V4L2_BUF_TYPE_VIDEO_CAPTURE uint32 = 1

	V4L2_FIELD_NONE uint32 = 1

	VIDIOC_S_FMT uintptr = 0xc0d05605
	VIDIOC_G_FMT uintptr = 0xc0d05604
	VIDIOC_S_CTRL uintptr = 0xc008561c

	V4L2_CID_KEEP_FORMAT uint32 = 0x0098f900

	PixelFormatYUV420 FourCC = 0x32315559 // 'YU12' (I420 / Planar YUV 4:2:0)
	PixelFormatYUYV   FourCC = 0x56595559 // 'YUYV' (YUV 4:2:2)
	PixelFormatMJPEG  FourCC = 0x47504a4d // 'MJPG'
)

type v4l2Control struct {
	id    uint32
	value int32
}

type Device struct {
	mu      sync.Mutex
	file    *os.File
	width   uint32
	height  uint32
	format  FourCC
	bufYU12 []byte
}

func Open(devicePath string) (*Device, error) {
	f, err := os.OpenFile(devicePath, os.O_RDWR|os.O_SYNC, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open v4l2 device %s: %w", devicePath, err)
	}

	dev := &Device{
		file: f,
	}

	// Query actual format configured on the v4l2 device
	buf := make([]byte, 208)
	binary.LittleEndian.PutUint32(buf[0:4], V4L2_BUF_TYPE_VIDEO_OUTPUT)
	fd := f.Fd()
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		VIDIOC_G_FMT,
		uintptr(unsafe.Pointer(&buf[0])),
	)

	if errno == 0 {
		dev.width = binary.LittleEndian.Uint32(buf[8:12])
		dev.height = binary.LittleEndian.Uint32(buf[12:16])
		dev.format = FourCC(binary.LittleEndian.Uint32(buf[16:20]))
		sizeImage := (dev.width * dev.height * 3) / 2
		if sizeImage > 0 {
			dev.bufYU12 = make([]byte, sizeImage)
		}
	}

	return dev, nil
}

func (d *Device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file != nil {
		err := d.file.Close()
		d.file = nil
		return err
	}
	return nil
}

func (d *Device) SetFormat(width, height uint32, format FourCC) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	var bytesPerLine uint32
	var sizeImage uint32

	switch format {
	case PixelFormatYUV420:
		bytesPerLine = width
		sizeImage = (width * height * 3) / 2
	case PixelFormatYUYV:
		bytesPerLine = width * 2
		sizeImage = width * height * 2
	case PixelFormatMJPEG:
		bytesPerLine = 0
		sizeImage = width * height
	default:
		bytesPerLine = width
		sizeImage = width * height
	}

	buf := make([]byte, 208)
	binary.LittleEndian.PutUint32(buf[0:4], V4L2_BUF_TYPE_VIDEO_OUTPUT)
	binary.LittleEndian.PutUint32(buf[8:12], width)
	binary.LittleEndian.PutUint32(buf[12:16], height)
	binary.LittleEndian.PutUint32(buf[16:20], uint32(format))
	binary.LittleEndian.PutUint32(buf[20:24], V4L2_FIELD_NONE)
	binary.LittleEndian.PutUint32(buf[24:28], bytesPerLine)
	binary.LittleEndian.PutUint32(buf[28:32], sizeImage)

	fd := d.file.Fd()

	// Ensure keep_format is disabled so v4l2loopback accepts the format update
	ctrl := v4l2Control{
		id:    V4L2_CID_KEEP_FORMAT,
		value: 0,
	}
	_, _, _ = syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		VIDIOC_S_CTRL,
		uintptr(unsafe.Pointer(&ctrl)),
	)

	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		VIDIOC_S_FMT,
		uintptr(unsafe.Pointer(&buf[0])),
	)

	// Update device geometry even if keep_format is locked
	if errno == 0 {
		d.width = width
		d.height = height
		d.format = format
		if format == PixelFormatYUV420 {
			d.bufYU12 = make([]byte, sizeImage)
		}
	} else if d.width == 0 || d.height == 0 {
		d.width = width
		d.height = height
		d.format = format
		if format == PixelFormatYUV420 {
			d.bufYU12 = make([]byte, sizeImage)
		}
		return fmt.Errorf("ioctl VIDIOC_S_FMT failed: %w", errno)
	}

	return nil
}

func (d *Device) WriteFrame(data []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.file.Write(data)
}

// WriteJPEGAsYU12 decodes a JPEG frame into planar YU12 (YUV420p), scales it to the device's
// exact width/height, and writes the exact buffer size expected by v4l2loopback
func (d *Device) WriteJPEGAsYU12(jpegData []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return fmt.Errorf("failed to decode jpeg: %w", err)
	}

	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	outW := int(d.width)
	outH := int(d.height)
	if outW <= 0 || outH <= 0 {
		outW = srcW
		outH = srcH
	}

	requiredSize := (outW * outH * 3) / 2
	if len(d.bufYU12) != requiredSize {
		d.bufYU12 = make([]byte, requiredSize)
	}

	ycbcr, isYCbCr := img.(*image.YCbCr)
	if isYCbCr && ycbcr.SubsampleRatio == image.YCbCrSubsampleRatio420 {
		if srcW == outW && srcH == outH {
			// Fast path: dimensions match and image is already YCbCr 4:2:0 planar
			yIdx := 0
			for y := 0; y < outH; y++ {
				srcOffset := ycbcr.YOffset(0, y)
				copy(d.bufYU12[yIdx:yIdx+outW], ycbcr.Y[srcOffset:srcOffset+outW])
				yIdx += outW
			}

			uIdx := outW * outH
			vIdx := outW * outH + (outW*outH)/4
			halfW := outW / 2
			halfH := outH / 2

			for y := 0; y < halfH; y++ {
				cOffset := ycbcr.COffset(0, y*2)
				copy(d.bufYU12[uIdx:uIdx+halfW], ycbcr.Cb[cOffset:cOffset+halfW])
				copy(d.bufYU12[vIdx:vIdx+halfW], ycbcr.Cr[cOffset:cOffset+halfW])
				uIdx += halfW
				vIdx += halfW
			}
		} else {
			// Proportional integer scaling with aspect ratio preservation (letterbox / pillarbox)
			var drawW, drawH int
			if srcW*outH > srcH*outW {
				drawW = outW
				drawH = (srcH * outW) / srcW
			} else {
				drawH = outH
				drawW = (srcW * outH) / srcH
			}

			drawW &= ^1
			drawH &= ^1
			if drawW < 2 {
				drawW = 2
			}
			if drawH < 2 {
				drawH = 2
			}
			if drawW > outW {
				drawW = outW
			}
			if drawH > outH {
				drawH = outH
			}

			offsetX := ((outW - drawW) / 2) & ^1
			offsetY := ((outH - drawH) / 2) & ^1

			// Initialize black background if bars are present (BT.601 limited range: Y=16, U=128, V=128)
			if offsetX > 0 || offsetY > 0 {
				yTotal := outW * outH
				for i := 0; i < yTotal; i++ {
					d.bufYU12[i] = 16
				}
				uvTotal := yTotal / 2
				for i := 0; i < uvTotal; i++ {
					d.bufYU12[yTotal+i] = 128
				}
			}

			uIdx := outW * outH
			vIdx := outW * outH + (outW*outH)/4
			halfDrawW := drawW / 2
			halfDrawH := drawH / 2
			halfOutW := outW / 2
			halfOffsetX := offsetX / 2
			halfOffsetY := offsetY / 2

			// Scale Y plane
			for dy := 0; dy < drawH; dy++ {
				srcY := (dy * srcH) / drawH
				srcRowOffset := ycbcr.YOffset(0, srcY)
				dstRowOffset := (offsetY + dy) * outW + offsetX
				for dx := 0; dx < drawW; dx++ {
					srcX := (dx * srcW) / drawW
					d.bufYU12[dstRowOffset+dx] = ycbcr.Y[srcRowOffset+srcX]
				}
			}

			// Scale U and V planes (subsampled 2x2)
			for dy := 0; dy < halfDrawH; dy++ {
				srcY := (dy * 2 * srcH) / drawH
				srcCOffset := ycbcr.COffset(0, srcY)
				dstRowOffset := (halfOffsetY + dy) * halfOutW + halfOffsetX
				for dx := 0; dx < halfDrawW; dx++ {
					srcX := (dx * 2 * srcW) / drawW
					d.bufYU12[uIdx+dstRowOffset+dx] = ycbcr.Cb[srcCOffset+srcX/2]
					d.bufYU12[vIdx+dstRowOffset+dx] = ycbcr.Cr[srcCOffset+srcX/2]
				}
			}
		}
	} else {
		// Generic conversion fallback with proportional scaling
		var drawW, drawH int
		if srcW*outH > srcH*outW {
			drawW = outW
			drawH = (srcH * outW) / srcW
		} else {
			drawH = outH
			drawW = (srcW * outH) / srcH
		}
		drawW &= ^1
		drawH &= ^1
		if drawW < 2 {
			drawW = 2
		}
		if drawH < 2 {
			drawH = 2
		}
		if drawW > outW {
			drawW = outW
		}
		if drawH > outH {
			drawH = outH
		}

		offsetX := ((outW - drawW) / 2) & ^1
		offsetY := ((outH - drawH) / 2) & ^1

		if offsetX > 0 || offsetY > 0 {
			yTotal := outW * outH
			for i := 0; i < yTotal; i++ {
				d.bufYU12[i] = 16
			}
			uvTotal := yTotal / 2
			for i := 0; i < uvTotal; i++ {
				d.bufYU12[yTotal+i] = 128
			}
		}

		uIdx := outW * outH
		vIdx := outW * outH + (outW*outH)/4
		halfOutW := outW / 2
		halfOffsetX := offsetX / 2
		halfOffsetY := offsetY / 2

		for dy := 0; dy < drawH; dy++ {
			srcY := (dy * srcH) / drawH
			dstRowOffset := (offsetY + dy) * outW + offsetX
			for dx := 0; dx < drawW; dx++ {
				srcX := (dx * srcW) / drawW
				r, g, b, _ := img.At(srcX, srcY).RGBA()
				r8 := uint8(r >> 8)
				g8 := uint8(g >> 8)
				b8 := uint8(b >> 8)

				// ITU-R BT.601 conversion
				yVal := uint8((66*int(r8) + 129*int(g8) + 25*int(b8) + 128)>>8 + 16)
				d.bufYU12[dstRowOffset+dx] = yVal

				if dx%2 == 0 && dy%2 == 0 {
					uVal := uint8((-38*int(r8) - 74*int(g8) + 112*int(b8) + 128)>>8 + 128)
					vVal := uint8((112*int(r8) - 94*int(g8) - 18*int(b8) + 128)>>8 + 128)

					uvRowOffset := (halfOffsetY + dy/2) * halfOutW + (halfOffsetX + dx/2)
					d.bufYU12[uIdx+uvRowOffset] = uVal
					d.bufYU12[vIdx+uvRowOffset] = vVal
				}
			}
		}
	}

	_, err = d.file.Write(d.bufYU12)
	return err
}
