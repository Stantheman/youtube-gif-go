package api

import (
	"errors"
	"net/url"
	"strconv"
)

// pubsubmessages is the structure after decoding the JSON
// published via Redis
type PubSubMessage struct {
	ID    string
	URL   string
	Start string
	Dur   string
}

func ValidateParams(form url.Values) (*PubSubMessage, error) {
	msg := PubSubMessage{Start: "0"}

	// validate it
	purl, err := url.Parse(form["url"][0])
	if err != nil {
		return nil, err
	}

	// for now
	if purl.Host != "www.youtube.com" {
		return nil, errors.New("not youtube: " + purl.Host)
	}

	msg.URL = form["url"][0]

	// make sure ss looks like a num
	if ss, ok := form["start"]; ok {
		if _, err := strconv.Atoi(ss[0]); err != nil {
			return nil, err
		}
		msg.Start = form["start"][0]
	}
	// make sure dur looks like a num
	if dur, ok := form["dur"]; ok {
		if _, err := strconv.Atoi(dur[0]); err != nil {
			return nil, err
		}
		msg.Dur = form["dur"][0]
	}

	return &msg, nil
}
