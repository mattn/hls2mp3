package main

import (
	"crypto/aes"
	"crypto/cipher"
	"embed"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/grafov/m3u8"
)

const name = "hls2mp3"

const version = "0.0.13"

var revision = "HEAD"

type segment struct {
	url      string
	keyURL   string
	seq      uint64
	duration float64
}

//go:embed static
var static embed.FS

func normalizeURL(url1, url2 string) string {
	if !strings.HasPrefix(url2, "http") {
		if strings.HasPrefix(url2, "/") {
			if u, err := url.Parse(url1); err == nil {
				u.Path = url2
				url2 = u.String()
			}
		} else {
			baseURL := url1[:strings.LastIndex(url1, "/")+1]
			url2 = baseURL + url2
		}
	}
	return url2
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
			segments = append(segments, segment{
				url:      normalizeURL(url, mediaPlaylist.Map.URI),
				keyURL:   "",
				seq:      0,
				duration: 0,
			})
		}
		for _, s := range mediaPlaylist.Segments {
			if s == nil {
				continue
			}
			joinKey := ""
			if s.Key != nil && s.Key.URI != "" {
				joinKey = normalizeURL(url, s.Key.URI)
			}
			segments = append(segments, segment{
				url:    normalizeURL(url, s.URI),
				keyURL: joinKey,
				seq:    s.SeqId,

				duration: s.Duration,
			})
		}
		return segments, nil
	} else if listType == m3u8.MASTER {
		masterPlaylist := playlist.(*m3u8.MasterPlaylist)
		for _, s := range masterPlaylist.Variants {
			if s == nil {
				continue
			}
			/*
				for _, variant := range masterPlaylist.Variants {
					if strings.HasPrefix(variant.VariantParams.Codecs, "mp4a") {
						codec = "audio/aac"
					}
				}
			*/
			return fetchM3U8(normalizeURL(url, s.URI))
		}
		return nil, errors.New("M3U8 not found")
	}

	return nil, errors.New("invalid M3U8 format")
}

func fetchKey(keyURL string) ([]byte, error) {
	req, _ := http.NewRequest("GET", keyURL, nil)
	/*
		for k, v := range headers {
			req.Header.Set(k, v)
		}
	*/
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("key fetch failed: %d", resp.StatusCode)
	}

	key, err := io.ReadAll(resp.Body)
	if err != nil || len(key) != 16 { // AES-128 key is 16 bytes
		return nil, fmt.Errorf("invalid key: %v", err)
	}

	return key, nil
}

func decryptAES128(data []byte, seq uint32, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	iv := make([]byte, aes.BlockSize)
	binary.BigEndian.PutUint32(iv[12:], seq)

	decrypter := cipher.NewCBCDecrypter(block, iv)
	decrypter.CryptBlocks(data, data)

	if len(data) > 0 {
		pad := int(data[len(data)-1])
		if pad > 0 && pad <= aes.BlockSize {
			data = data[:len(data)-pad]
		}
	}
	return data, nil
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
	joinKey := []byte{}

	for {
		s, ok := <-q
		if !ok {
			break
		}
		log.Println("serve", s.url)

		resp, err := http.Get(s.url)
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

		if s.keyURL != "" {
			if key, err := fetchKey(s.keyURL); err == nil {
				joinKey = key
			}
		}

		if len(joinKey) > 0 {
			if d, err := decryptAES128(data, uint32(s.seq), joinKey); err == nil {
				data = d
			}
		}

		flushed := false
		for i := 0; i < len(data)-1; i++ {
			if data[i] == 0xFF && (data[i+1]&0xF0) == 0xF0 {
				_, err = w.Write(data[i:])
				if err != nil {
					log.Println(err)
					return
				} else {
					flusher.Flush()
					flushed = true
				}
				break
			}
		}

		if !flushed {
			_, err = w.Write(data)
			if err != nil {
				log.Println(err)
				return
			} else {
				flusher.Flush()
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
	sub, _ := fs.Sub(static, "static")
	http.Handle("/", http.FileServer(http.FS(sub)))
	http.ListenAndServe(":8080", nil)
}
