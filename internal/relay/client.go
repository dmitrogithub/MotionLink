package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"

	"motionlink/internal/auth"
)

// OfferHandler answers one offer; it is implemented by signaling.Answerer.
type OfferHandler interface {
	Answer(source string, offer auth.Offer, extraICEServers []webrtc.ICEServer) (auth.Answer, error)
}

// Client keeps the host registered in a relay room and answers offers that
// arrive through it.
type Client struct {
	base    *url.URL
	room    string
	handler OfferHandler
	logger  *log.Logger
	http    *http.Client
}

func NewClient(relayURL, key string, handler OfferHandler, logger *log.Logger) (*Client, error) {
	base, err := ParseURL(relayURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:    base,
		room:    auth.RoomID(key),
		handler: handler,
		logger:  logger,
		http:    &http.Client{Timeout: pollTimeout + 15*time.Second},
	}, nil
}

// ParseURL accepts "relay.example.com", "relay.example.com:8443" or a full
// http(s) URL and returns its base URL.
func ParseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid relay address %q", raw)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed, nil
}

// Run polls until ctx is cancelled, backing off while the relay is down.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	online := false
	for ctx.Err() == nil {
		err := c.pollOnce(ctx)
		if err == nil {
			if !online {
				c.logger.Printf("relay: registered at %s (room %s…)", c.base.Host, c.room[:8])
				online = true
			}
			backoff = time.Second
			continue
		}
		if ctx.Err() != nil {
			return
		}
		online = false
		c.logger.Printf("relay: %v; retrying in %s", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (c *Client) pollOnce(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("poll"), nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusOK:
	default:
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("poll returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	var poll PollResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxRelayBody)).Decode(&poll); err != nil {
		return fmt.Errorf("decode relay offer: %w", err)
	}
	go c.answer(ctx, poll)
	return nil
}

func (c *Client) answer(ctx context.Context, poll PollResponse) {
	reply := Reply{Status: http.StatusOK}
	var offer auth.Offer
	if err := json.Unmarshal(poll.Offer, &offer); err != nil {
		reply = errorReply(&auth.Error{Status: http.StatusBadRequest, Code: "bad-request", Message: "invalid offer JSON"})
	} else if answer, err := c.handler.Answer("relay:"+poll.From, offer, poll.ICEServers); err != nil {
		reply = errorReply(err)
	} else {
		reply.Body, _ = json.Marshal(answer)
	}

	payload, _ := json.Marshal(reply)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("answer/"+url.PathEscape(poll.ID)), bytes.NewReader(payload))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		c.logger.Printf("relay: deliver answer: %v", err)
		return
	}
	_ = response.Body.Close()
	if response.StatusCode == http.StatusGone {
		c.logger.Printf("relay: the client stopped waiting before the answer arrived")
	}
}

func (c *Client) endpoint(suffix string) string {
	return c.base.String() + "/api/relay/" + c.room + "/" + suffix
}

func errorReply(err error) Reply {
	var authError *auth.Error
	if !errors.As(err, &authError) {
		authError = &auth.Error{Status: http.StatusInternalServerError, Code: "internal", Message: err.Error()}
	}
	body, _ := json.Marshal(authError)
	return Reply{Status: authError.Status, Body: body}
}
