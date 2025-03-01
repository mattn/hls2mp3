package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"time"

	"github.com/grafov/m3u8"
)

const name = "hls2mp3"

const version = "0.0.1"

var revision = "HEAD"

func fetchM3U8(url string) ([]string, int, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	playlist, listType, err := m3u8.DecodeFrom(resp.Body, true)
	if err != nil {
		return nil, 0, err
	}

	var duration int

	if listType == m3u8.MEDIA {
		mediaPlaylist := playlist.(*m3u8.MediaPlaylist)
		var tsUrls []string
		for _, segment := range mediaPlaylist.Segments {
			if segment == nil {
				continue
			}
			duration += int(segment.Duration)
			tsURL := segment.URI
			if !strings.HasPrefix(tsURL, "http") {
				baseURL := url[:strings.LastIndex(url, "/")+1]
				tsURL = baseURL + tsURL
			}
			tsUrls = append(tsUrls, tsURL)
		}
		return tsUrls, duration, nil
	}
	return nil, 0, fmt.Errorf("invalid M3U8 format")
}

func serveMP3(w http.ResponseWriter, r *http.Request) {
	hlsURL := r.URL.Query().Get("url")

	q := make(chan string)

	go func() {
		defer close(q)
		for {
			tsURLs, duration, err := fetchM3U8(hlsURL)
			if err != nil {
				http.Error(w, "Failed to fetch M3U8", http.StatusInternalServerError)
				return
			}
			for _, tsURL := range tsURLs {
				q <- tsURL
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.NewTimer(time.Duration(duration) * time.Second).C:
			}
		}
	}()

	w.Header().Set("Content-Type", "audio/mpeg")

	flusher, _ := w.(http.Flusher)
	cw := httputil.NewChunkedWriter(w)

	for {
		tsURL, ok := <-q
		if !ok {
			break
		}

		var mp3Data bytes.Buffer
		resp, err := http.Get(tsURL)
		if err != nil {
			http.Error(w, "Failed to extract MP3", http.StatusInternalServerError)
			break
		}
		defer resp.Body.Close()

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "Failed to extract MP3", http.StatusInternalServerError)
			break
		}
		var result bytes.Buffer
		for i := range len(data) {
			if data[i] == 0xFF && (data[i+1]&0xF0) == 0xF0 {
				result.Write(data[i:])
				break
			}
		}
		mp3Data.Write(result.Bytes())
		_, err = cw.Write(result.Bytes())
		if err != nil {
			http.Error(w, "Failed to write MP3", http.StatusInternalServerError)
			break
		}
		flusher.Flush()
	}
}

func main() {
	var ver bool
	flag.BoolVar(&ver, "version", false, "show version")
	flag.Parse()

	if ver {
		fmt.Println(version)
		os.Exit(0)
	}

	http.HandleFunc("/audio", serveMP3)
	http.ListenAndServe(":8080", nil)
}
