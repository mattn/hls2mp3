# hls2mp3

hls2mp3 is a proxy server that converts HLS streams to audio stream.

## Installation

```sh
git install github.com/mattn/hls2mp3@latest
```

## Usage

```sh
$ ./hls2mp3
```

This server makes http audio stream via hls streams

```
https://my-server.com:8080/audio?url=https://example.com/audio.m3u8
```

## License

MIT

## Author

Yasuhiro Matsumoto (a.k.a. mattn)
