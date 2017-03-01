package iiif

import (
	"fmt"
	"image"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/t11e/picaxe/imageops"
)

type InvalidSpec struct {
	Message string
}

func (e InvalidSpec) Error() string {
	return e.Message
}

const (
	RegionStringFull   = "full"
	RegionStringSquare = "square"
	RegionStringPct    = "pct:"
)

type RegionKind int

const (
	RegionKindFull RegionKind = iota
	RegionKindSquare
	RegionKindAbsolute
	RegionKindRelative
)

type Region struct {
	Kind     RegionKind
	Absolute *image.Rectangle
	Relative *imageops.RelativeRegion
}

const (
	SizeStringFull = "full"
	SizeStringMax  = "max"
	SizeStringPct  = "pct:"
)

type SizeKind int

const (
	SizeKindFull SizeKind = iota
	SizeKindMax
	SizeKindAbsolute
	SizeKindRelative
)

type Size struct {
	Kind       SizeKind
	AbsWidth   *int
	AbsHeight  *int
	AbsBestFit bool
	Relative   *float64
}

func (size Size) CalculateDimensions(in, maxSize image.Point) (image.Point, error) {
	var result image.Point
	switch size.Kind {
	case SizeKindFull:
		result = in
	case SizeKindMax:
		if in.X > maxSize.X || in.Y > maxSize.Y {
			result = imageops.FitDimensions(in, &maxSize.X, &maxSize.Y)
		} else {
			result = in
		}
	case SizeKindAbsolute:
		if size.AbsBestFit || size.AbsWidth == nil || size.AbsHeight == nil {
			result = imageops.FitDimensions(in, size.AbsWidth, size.AbsHeight)
		} else {
			result = image.Pt(*size.AbsWidth, *size.AbsHeight)
		}
	case SizeKindRelative:
		w := round(float64(in.X) * *size.Relative)
		h := round(float64(in.Y) * *size.Relative)
		result = imageops.FitDimensions(in, &w, &h)
	default:
		panic("Invalid size specification")
	}
	return checkDimensions(maxSize, result)
}

func checkDimensions(maxSize, size image.Point) (image.Point, error) {
	if size.X > maxSize.X || size.Y > maxSize.Y {
		return image.Point{}, fmt.Errorf("(%d, %d) exceeds maximum allowed dimensions (%d, %d)",
			size.X, size.Y, maxSize.X, maxSize.Y)
	}
	return size, nil
}

type Format string

const (
	FormatDefault Format = ""
	FormatJPEG           = "jpg"
	FormatPNG            = "png"
	FormatGIF            = "gif"
)

type Request struct {
	Identifier          string
	Region              Region
	Size                Size
	Format              Format
	AutoOrient          bool
	TrimBorder          bool
	TrimBorderFuzziness float64
}

var specRegexp = regexp.MustCompile(`([^/]+)/([^/]+)/([^/]+)/([^/]+)/([^.]+)\.([^?]+)(?:\?(.*))?$`)

func ParseSpec(spec string) (*Request, error) {
	parts := specRegexp.FindStringSubmatch(spec)
	if len(parts) != 8 {
		return nil, InvalidSpec{
			Message: fmt.Sprintf("not a valid spec: %q", spec),
		}
	}

	var req Request

	if id, err := url.QueryUnescape(parts[1]); err == nil {
		req.Identifier = id
	} else {
		return nil, err
	}

	if err := parseRegion(parts[2], &req.Region); err != nil {
		return nil, err
	}

	if err := parseSize(parts[3], &req.Size); err != nil {
		return nil, err
	}

	if rotation := parts[4]; rotation != "" {
		switch rotation {
		case "0":
			// OK
		default:
			return nil, InvalidSpec{
				Message: fmt.Sprintf("unsupported rotation %q", rotation),
			}
		}
	}

	if quality := parts[5]; quality != "" {
		switch quality {
		case "color", "default":
			// OK
		default:
			return nil, InvalidSpec{
				Message: fmt.Sprintf("unsupported quality %q", quality),
			}
		}
	}

	if format := parts[6]; format != "" {
		name, ok := formatNameMap[format]
		if !ok {
			return nil, InvalidSpec{
				Message: fmt.Sprintf("unsupported format %q", format),
			}
		}
		req.Format = name
	} else {
		req.Format = FormatDefault
	}

	if parts[7] != "" {
		values, err := url.ParseQuery(parts[7])
		if err != nil {
			return nil, InvalidSpec{
				Message: fmt.Sprintf("invalid query string %q", parts[7]),
			}
		}

		if t := values.Get("trimBorder"); t != "" {
			req.TrimBorderFuzziness, err = parseFloat(t, 0, 0.999)
			if err != nil {
				return nil, err
			}
			req.TrimBorder = req.TrimBorderFuzziness > 0
		}

		if t := values.Get("autoOrient"); t != "" {
			req.AutoOrient, err = parseBoolean(t)
			if err != nil {
				return nil, err
			}
		}
	}

	return &req, nil
}

func parseBoolean(value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, InvalidSpec{
		Message: fmt.Sprintf("not a boolean value: %q", value),
	}
}

func parseFloat(value string, min, max float64) (float64, error) {
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, InvalidSpec{
			Message: fmt.Sprintf("not a floating-point value: %q", value),
		}
	}
	if f < min || f > max {
		return 0, InvalidSpec{
			Message: fmt.Sprintf("value outside of range %f..%f: %f", min, max, f),
		}
	}
	return f, nil
}

func parseRegion(regionValue string, region *Region) error {
	switch regionValue {
	case RegionStringFull, "":
		region.Kind = RegionKindFull
		return nil
	case RegionStringSquare:
		region.Kind = RegionKindSquare
		return nil
	}

	if strings.HasPrefix(regionValue, RegionStringPct) {
		var err error
		region.Kind = RegionKindRelative
		region.Relative, err = parsePercentageCoords(regionValue[len(RegionStringPct):])
		if err != nil {
			return err
		}
		return nil
	}

	var err error
	region.Kind = RegionKindAbsolute
	region.Absolute, err = parseRectangle(regionValue)
	return err
}

func parseSize(sizeValue string, size *Size) error {
	switch sizeValue {
	case SizeStringFull, "":
		size.Kind = SizeKindFull
		return nil
	case SizeStringMax:
		size.Kind = SizeKindMax
		return nil
	}

	if strings.HasPrefix(sizeValue, SizeStringPct) {
		pcnt, err := parsePercentage(sizeValue[len(SizeStringPct):])
		if err != nil {
			return err
		}

		size.Kind = SizeKindRelative
		size.Relative = &pcnt
		return nil
	}

	var err error
	size.Kind = SizeKindAbsolute
	size.AbsWidth, size.AbsHeight, size.AbsBestFit, err = parseWidthHeight(sizeValue)
	return err
}

func round(f float64) int {
	return int(math.Floor(f + .5))
}

var formatNameMap map[string]Format

func init() {
	formats := []Format{FormatJPEG, FormatPNG, FormatGIF}
	formatNameMap = make(map[string]Format, len(formats))
	for _, n := range formats {
		formatNameMap[string(n)] = n
	}
}
