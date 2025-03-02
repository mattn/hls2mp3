package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/grafov/m3u8"
)

const name = "hls2mp3"

const version = "0.0.5"

var revision = "HEAD"

type segment struct {
	tsURL    string
	duration float64
}

func fetchM3U8(url string) ([]segment, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	playlist, listType, err := m3u8.DecodeFrom(resp.Body, true)
	if err != nil {
		return nil, err
	}

	if listType == m3u8.MEDIA {
		mediaPlaylist := playlist.(*m3u8.MediaPlaylist)
		var segments []segment

		if mediaPlaylist.Map != nil {
			tsURL := mediaPlaylist.Map.URI
			if !strings.HasPrefix(tsURL, "http") {
				baseURL := url[:strings.LastIndex(url, "/")+1]
				tsURL = baseURL + tsURL
			}
			segments = append(segments, segment{
				tsURL:    tsURL,
				duration: 0,
			})
		}

		for _, s := range mediaPlaylist.Segments {
			if s == nil {
				continue
			}
			tsURL := s.URI
			if !strings.HasPrefix(tsURL, "http") {
				baseURL := url[:strings.LastIndex(url, "/")+1]
				tsURL = baseURL + tsURL
			}
			segments = append(segments, segment{
				tsURL:    tsURL,
				duration: s.Duration,
			})
		}
		return segments, nil
	}

	return nil, fmt.Errorf("invalid M3U8 format")
}

func serveMP3(w http.ResponseWriter, r *http.Request) {
	hlsURL := r.URL.Query().Get("url")

	q := make(chan segment, 10)

	go func() {
		defer close(q)
		for {
			segments, err := fetchM3U8(hlsURL)
			if err != nil {
				http.Error(w, "Failed to fetch M3U8", http.StatusInternalServerError)
				return
			}
			duration := float64(0)
			for _, s := range segments {
				q <- s
				duration += s.duration
			}
			log.Println("sleep", duration)
			select {
			case <-r.Context().Done():
				return
			case <-time.NewTimer(time.Duration(duration * float64(time.Second))).C:
				log.Println("break")
			}
		}
	}()

	w.Header().Set("Content-Type", "audio/mpeg")
	flusher, _ := w.(http.Flusher)

	for {
		s, ok := <-q
		if !ok {
			break
		}
		log.Println("serve", s.tsURL)

		resp, err := http.Get(s.tsURL)
		if err != nil {
			http.Error(w, "Failed to extract MP3", http.StatusInternalServerError)
			break
		}

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			resp.Body.Close()
			http.Error(w, "Failed to extract MP3", http.StatusInternalServerError)
			break
		}
		resp.Body.Close()

		for i := range len(data) {
			if data[i] == 0xFF && (data[i+1]&0xF0) == 0xF0 {
				_, err = w.Write(data[i:])
				if err != nil {
					http.Error(w, "Failed to write MP3", http.StatusInternalServerError)
				} else {
					flusher.Flush()
				}
				break
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.NewTimer(time.Duration(s.duration) * time.Second).C:
		}
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
