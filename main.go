package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/etherlabsio/go-m3u8/m3u8"
	"github.com/gookit/goutil/maputil"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/rs/zerolog/pkgerrors"
)

type Stitched struct {
	StartDate time.Time `json:"start_date"`
	RollType  string    `json:"roll_type"`
	PodLength int       `json:"pod_length"`
}

type H = map[string]any

const (
	graphURL          = "https://gql.twitch.tv/gql"
	masterPlaylistURL = "https://usher.ttvnw.net/api/channel/hls/%s.m3u8?token=%s&sig=%s"

	playlistInterval = 1 * time.Minute
	stitchedInterval = 2 * time.Second
)

var (
	clientID    = os.Getenv("CLIENT_ID")
	channelName = os.Getenv("CHANNEL_NAME")

	client = http.Client{
		Timeout: 5 * time.Second,
	}

	currentStitched *Stitched
)

func getAccessToken(channelName string) (string, string, error) {
	s := fmt.Sprintf(
		`{"query":"{streamPlaybackAccessToken(channelName:\"%s\",params:{platform:\"web\",playerBackend:\"mediaplayer\",playerType:\"site\"}){signature,value}}"}`,
		channelName,
	)

	req, err := http.NewRequest("POST", graphURL, strings.NewReader(s))

	if err != nil {
		return "", "", err
	}

	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Content-Type", "text/plain")

	resp, err := client.Do(req)

	if err != nil {
		return "", "", err
	}

	var out H

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}

	token := maputil.DeepGet(out, "data.streamPlaybackAccessToken.value").(string)
	signature := maputil.DeepGet(out, "data.streamPlaybackAccessToken.signature").(string)

	return token, signature, nil
}

func fetchPlaylistURL() (string, error) {
	token, signature, err := getAccessToken(channelName)

	if err != nil {
		return "", err
	}

	resp, err := client.Get(
		fmt.Sprintf(masterPlaylistURL, channelName, url.QueryEscape(token), signature),
	)

	if err != nil {
		return "", err
	}

	switch resp.StatusCode {
	case http.StatusOK:
		p, err := m3u8.Read(resp.Body)

		if err != nil {
			return "", err
		}

		for _, pi := range p.Playlists() {
			return pi.URI, nil
		}
	}

	return "", nil
}

func fetchPlaylist(url string) (*m3u8.Playlist, error) {
	resp, err := http.Get(url)

	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return m3u8.Read(resp.Body)
	}

	return nil, nil
}

func fetchStitched(url string) (*Stitched, error) {
	p, err := fetchPlaylist(url)

	if err != nil {
		return nil, err
	}

	for _, item := range p.Items {
		switch v := item.(type) {
		case *m3u8.DateRangeItem:
			switch *v.Class {
			case "twitch-stitched-ad":
				rollType := strings.ToUpper(v.ClientAttributes["X-TV-TWITCH-AD-ROLL-TYPE"])

				if rollType == "PREROLL" {
					break
				}

				podLength, err := strconv.Atoi(v.ClientAttributes["X-TV-TWITCH-AD-POD-LENGTH"])

				if err != nil {
					return nil, err
				}

				startDate, err := time.Parse(time.RFC3339, v.StartDate)

				if err != nil {
					return nil, err
				}

				return &Stitched{startDate, rollType, podLength}, nil
			}
		}
	}

	return nil, nil
}

func startWebServer() error {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().
			Set("Content-Type", "application/json")

		json.NewEncoder(w).
			Encode(H{
				"data": currentStitched,
			})
	})

	return http.ListenAndServe(":3000", handler)
}

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack

	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out: os.Stdout,
	})

	go startWebServer()

	for {
		log := log.With().
			Str("channel_name", channelName).
			Logger()

		log.Debug().
			Msg("Fetching playlist url...")

		url, err := fetchPlaylistURL()

		if err != nil {
			log.Error().
				Err(err).
				Msg("An error occured while fetching playlist url")
		}

		if url != "" {
			log := log.With().
				Str("playlist_url", url).
				Logger()

			log.Info().
				Msg("Channel playlist found")

			for {
				log.Debug().
					Msg("Fetching stitched...")

				stitched, err := fetchStitched(url)

				if err != nil {
					log.Error().
						Err(err).
						Msg("An error occured while fetching stitched")

					break
				}

				currentStitched = stitched

				log.Debug().
					Interface("stitched", stitched).
					Msg("Fetched stitched")

				time.Sleep(stitchedInterval)
			}
		}

		time.Sleep(playlistInterval)
	}
}
