package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/eemeyer/chi"
	"github.com/eemeyer/chi/middleware"
)

type contextKey int

const (
	httpClientKey contextKey = iota
)

type Server struct{}

func NewServer() *Server {
	return &Server{}
}

func (s *Server) Run(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Unable to listen on address %q: %s", address, err.Error())
	}
	log.Printf("Listening on %s", address)
	return http.Serve(listener, s.Handler())
}

func (s *Server) Handler() *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.CloseNotify)
	r.Use(middleware.WithValue(httpClientKey, &http.Client{Timeout: time.Duration(10 * time.Second)}))
	r.Use(middleware.Timeout(30 * time.Second))
	r.Get("/api/picaxe/ping", func(resp http.ResponseWriter, _ *http.Request) {
		resp.WriteHeader(200)
		_, _ = resp.Write([]byte("picaxe"))
	})
	r.Get("/api/picaxe/v1/:identifier/:region/:size/:rotation/*", s.handleResize)
	return r
}

func (s *Server) handleResize(resp http.ResponseWriter, req *http.Request) {
	etag := md5sum(req.URL.Query().Encode())
	if match := req.Header.Get("If-None-Match"); match != "" {
		if strings.Contains(match, etag) {
			resp.WriteHeader(http.StatusNotModified)
			return
		}
	}
	src, spec, err := makeProcessingSpec(req)
	if err != nil {
		resp.WriteHeader(http.StatusBadRequest)
		resp.Write([]byte(err.Error()))
		return
	}
	httpClient, _ := req.Context().Value(httpClientKey).(http.Client)
	imgResp, err := httpClient.Get(src)
	if err != nil {
		resp.WriteHeader(http.StatusInternalServerError)
		resp.Write([]byte(fmt.Sprintf("Unable to get %s: %v", src, err)))
		return
	}
	defer imgResp.Body.Close()
	if http.StatusOK != imgResp.StatusCode {
		resp.WriteHeader(imgResp.StatusCode)
		resp.Write([]byte(fmt.Sprintf("Unable to get %s. Got %s", src, imgResp.Status)))
		return
	}

	sourceImg, err := ioutil.ReadAll(imgResp.Body)
	if err != nil {
		resp.WriteHeader(http.StatusInternalServerError)
		resp.Write([]byte(fmt.Sprintf("Unable to retrieve %s: %v", src, err)))
		return
	}

	reader := bytes.NewReader(sourceImg)
	buffer := bytes.NewBuffer(make([]byte, 0, 1024*50))
	if err = ProcessImage(reader, buffer, *spec); err != nil {
		resp.WriteHeader(http.StatusInternalServerError)
		resp.Write([]byte(fmt.Sprintf("Error processing %s: %v", src, err)))
		return
	}

	resp.Header().Set("Content-type", "image/png")
	resp.Header().Set("Cache-Control", fmt.Sprintf("max-age=%d", 365*24*60*60))
	resp.Header().Set("ETag", etag)
	resp.WriteHeader(http.StatusOK)
	resp.Write(buffer.Bytes())
}

var regionRegex = regexp.MustCompile(`(?P<full>full)|(?P<square>square)`)
var sizeRegex = regexp.MustCompile(`(?P<max>max)|!(?P<bestwh>\d+,\d+)|(?P<wh>\d+,\d+)`)
var formatRegexp = regexp.MustCompile("jpg|png|gif")

func makeProcessingSpec(req *http.Request) (string, *ProcessingSpec, error) {
	identifier := strings.TrimSpace(chi.URLParam(req, "identifier"))
	src, err := url.Parse(identifier)
	if err != nil || !map[string]bool{"http": true, "https": true}[src.Scheme] {
		return "", nil, fmt.Errorf("invalid identifier '%s'", identifier)
	}

	regionName, region := namedMatch(regionRegex, chi.URLParam(req, "region"))
	if region == "" {
		return "", nil, fmt.Errorf("invalid or unsupported region '%s'", chi.URLParam(req, "region"))
	}

	sizeName, size := namedMatch(sizeRegex, chi.URLParam(req, "size"))
	if size == "" {
		return "", nil, fmt.Errorf("invalid size '%s'", chi.URLParam(req, "size"))
	}

	if rotation := chi.URLParam(req, "rotation"); rotation != "0" {
		return "", nil, fmt.Errorf("invalid or unsupported rotation '%s'", rotation)
	}
	qf := strings.Split(chi.URLParam(req, "*"), ".")
	quality := qf[0]
	format := qf[1]
	if quality != "default" {
		return "", nil, fmt.Errorf("invalid or unsupported quality '%s'", quality)
	}
	if !formatRegexp.MatchString(format) {
		return "", nil, fmt.Errorf("invalid or unsupported format '%s'", format)
	}

	spec := ProcessingSpec{
		Format:               ImageFormatPng,
		Trim:                 TrimModeNone,
		TrimFuzzFactor:       0.5,
		Scale:                ScaleModeNone,
		ScaleWidth:           0,
		ScaleHeight:          0,
		Crop:                 CropModeNone,
		CropWidth:            0,
		CropHeight:           0,
		NormalizeOrientation: true,
		Quality:              0.9,
	}

	switch format {
	case "jpg":
		spec.Format = ImageFormatJpeg
	case "png":
		spec.Format = ImageFormatPng
	case "gif":
		spec.Format = ImageFormatGif
	default:
		panic(format)
	}

	switch regionName {
	case "square":
		spec.Crop = CropModeCenter
		spec.Trim = TrimModeFuzzy
	case "full":
		spec.Crop = CropModeNone
		spec.Trim = TrimModeNone
	default:
		panic(regionName)
	}

	switch sizeName {
	case "max":
		spec.Scale = ScaleModeNone
	case "bestwh":
		spec.Scale = ScaleModeDown
	case "wh":
		spec.Scale = ScaleModeCover
	default:
		panic(sizeName)
	}
	if strings.Contains(size, ",") {
		wh := strings.Split(size, ",")
		w := wh[0]
		h := wh[1]
		d, err := strconv.Atoi(w)
		if err != nil {
			panic("cannot convert w")
		}
		spec.CropWidth = d
		spec.ScaleWidth = d
		d, err = strconv.Atoi(h)
		if err != nil {
			panic("cannot convert h")
		}
		spec.CropHeight = d
		spec.ScaleHeight = d
	}
	return identifier, &spec, nil
}

func namedMatch(exp *regexp.Regexp, input string) (string, string) {
	matches := exp.FindStringSubmatch(input)
	if matches == nil {
		return "", ""
	}
	names := exp.SubexpNames()
	for i, value := range matches[1:] {
		if value != "" {
			return names[1:][i], value
		}
	}
	return "", ""
}

func md5sum(query string) string {
	hasher := md5.New()
	hasher.Write([]byte(query))
	return hex.EncodeToString(hasher.Sum(nil))
}
