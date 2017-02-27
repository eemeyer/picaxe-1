package iiif

import (
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log"

	"github.com/t11e/picaxe/imageops"
)

//go:generate sh -c "mockery -name='Processor|ProcessorFactory' -case=underscore"

type Processor interface {
	Process(io.ReadSeeker, io.Writer) error
}

type Params map[string]string

type ProcessorFactory interface {
	NewProcessor(params Params) (Processor, error)
}

type processorFactory struct{}

// NewProcessor implements ProcessorFactory.
func (processorFactory) NewProcessor(params Params) (Processor, error) {
	req, err := RequestFromParams(params)
	if err != nil {
		return nil, err
	}
	log.Printf("params %#v", params)
	log.Printf("-> req %#v", req)
	return processor{req: *req}, nil
}

type processor struct {
	req Request
}

// Process implements Processor.
func (p processor) Process(r io.ReadSeeker, w io.Writer) error {
	req := p.req

	img, _, err := image.Decode(r)
	if err != nil {
		return err
	}

	r.Seek(0, 0)
	metadata := imageops.NewMetadataFromReader(r)
	if metadata.Exif != nil {
		if tag, e := metadata.Exif.Get("Orientation"); e == nil {
			img = imageops.NormalizeOrientation(img, tag.String())
		}
	}

	img = imageops.Trim(img, 0.1)

	switch req.Region.Kind {
	case RegionKindAbsolute:
		img = imageops.CropRect(img, *req.Region.Absolute)
	case RegionKindRelative:
		img = imageops.CropRelative(img, *req.Region.Relative)
	case RegionKindSquare:
		img = imageops.CropSquare(img)
	}

	dims, err := req.Size.CalculateDimensions(img.Bounds(), image.Pt(6000, 6000))
	if err != nil {
		return err
	}
	img = imageops.Scale(img, dims)

	switch req.Format {
	case FormatDefault, FormatPNG:
		return png.Encode(w, img)
	case FormatJPEG:
		return jpeg.Encode(w, img, &jpeg.Options{
			Quality: 98,
		})
	case FormatGIF:
		return gif.Encode(w, img, &gif.Options{
			NumColors: 256,
			Quantizer: nil,
			Drawer:    nil,
		})
	}

	return fmt.Errorf("Unexpected format %q", req.Format)
}

var DefaultProcessorFactory = processorFactory{}