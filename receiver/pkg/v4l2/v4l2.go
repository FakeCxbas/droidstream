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

	PixelFormatYUV420 FourCC = 0x32315559 // 'YU12' (I420 / Planar YUV 4:2:0)
	PixelFormatYUYV   FourCC = 0x56595559 // 'YUYV' (YUV 4:2:2)
	PixelFormatMJPEG  FourCC = 0x47504a4d // 'MJPG'
)

type Device struct {
	mu     sync.Mutex
	file   *os.File
	width  uint32
	height uint32
	format FourCC
	bufYU12 []byte
}

func Open(devicePath string) (*Device, error) {
	f, err := os.OpenFile(devicePath, os.O_RDWR|os.O_SYNC, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open v4l2 device %s: %w", devicePath, err)
	}

	return &Device{
		file: f,
	}, nil
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
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		VIDIOC_S_FMT,
		uintptr(unsafe.Pointer(&buf[0])),
	)

	if errno != 0 {
		return fmt.Errorf("ioctl VIDIOC_S_FMT failed: %w", errno)
	}

	d.width = width
	d.height = height
	d.format = format
	if format == PixelFormatYUV420 {
		d.bufYU12 = make([]byte, sizeImage)
	}

	return nil
}

func (d *Device) WriteFrame(data []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.file.Write(data)
}

// WriteJPEGAsYU12 decodes a JPEG frame into planar YU12 (YUV420p) and writes it into v4l2 device
func (d *Device) WriteJPEGAsYU12(jpegData []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return fmt.Errorf("failed to decode jpeg: %w", err)
	}

	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	requiredSize := (w * h * 3) / 2

	if len(d.bufYU12) != requiredSize {
		d.bufYU12 = make([]byte, requiredSize)
	}

	ycbcr, isYCbCr := img.(*image.YCbCr)
	if isYCbCr && ycbcr.SubsampleRatio == image.YCbCrSubsampleRatio420 {
		// Fast path: image is already YCbCr 4:2:0 planar!
		yIdx := 0
		for y := 0; y < h; y++ {
			srcOffset := ycbcr.YOffset(0, y)
			copy(d.bufYU12[yIdx:yIdx+w], ycbcr.Y[srcOffset:srcOffset+w])
			yIdx += w
		}

		uIdx := w * h
		vIdx := w * h + (w*h)/4
		halfW := w / 2
		halfH := h / 2

		for y := 0; y < halfH; y++ {
			cOffset := ycbcr.COffset(0, y*2)
			copy(d.bufYU12[uIdx:uIdx+halfW], ycbcr.Cb[cOffset:cOffset+halfW])
			copy(d.bufYU12[vIdx:vIdx+halfW], ycbcr.Cr[cOffset:cOffset+halfW])
			uIdx += halfW
			vIdx += halfW
		}
	} else {
		// Generic conversion fallback
		yOffset := 0
		uOffset := w * h
		vOffset := w * h + (w*h)/4

		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				// Convert 16-bit color to 8-bit
				r8 := uint8(r >> 8)
				g8 := uint8(g >> 8)
				b8 := uint8(b >> 8)

				// Standard ITU-R BT.601 conversion
				yVal := uint8((66*int(r8) + 129*int(g8) + 25*int(b8) + 128)>>8 + 16)
				d.bufYU12[yOffset+y*w+x] = yVal

				if x%2 == 0 && y%2 == 0 {
					uVal := uint8((-38*int(r8) - 74*int(g8) + 112*int(b8) + 128)>>8 + 128)
					vVal := uint8((112*int(r8) - 94*int(g8) - 18*int(b8) + 128)>>8 + 128)

					uvIdx := (y/2)*(w/2) + (x / 2)
					d.bufYU12[uOffset+uvIdx] = uVal
					d.bufYU12[vOffset+uvIdx] = vVal
				}
			}
		}
	}

	_, err = d.file.Write(d.bufYU12)
	return err
}
