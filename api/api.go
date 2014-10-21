package api

import (
	"errors"
	"github.com/mccoyst/validate"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

// pubsubmessages is the structure after decoding the JSON
// published via Redis. Contains API info and meta (id, prevDir)
// do i do c_x, c_y, c_w, c_h or convert to JSON?
type PubSubMessage struct {
	ID      string
	PrevDir string
	URL     string `validate:"checkURL"`
	Start   string `validate:"isOptionalNonNegative"`
	Dur     string `validate:"isOptionalPositive"`
	Cx      string `validate:"isOptionalPositive"`
	Cy      string `validate:"isOptionalPositive"`
	Cw      string `validate:"isOptionalPositive"`
	Ch      string `validate:"isOptionalPositive"`
}

func ValidateParams(form url.Values) (*PubSubMessage, []error) {
	// start being set to 0 is good
	msg := PubSubMessage{Start: "0", PrevDir: ""}

	// I am so freaking sorry. This loops over the PubSubMessage struct,
	// checks if there's an equivalent message in the form values map,
	// and if so, sets it.
	s := reflect.ValueOf(&msg).Elem()
	typeOfT := s.Type()
	for i := 0; i < s.NumField(); i++ {
		f := s.Field(i)
		name := typeOfT.Field(i).Name
		// guh
		if name == "ID" || name == "PrevDir" {
			continue
		}
		// if this element of PubSubMessage exists in the form data...
		if item, ok := form[strings.ToLower(name)]; ok {
			f.SetString(item[0])
		}
	}

	// check the the params look like numbers and urls
	vd := make(validate.V)
	vd["isOptionalPositive"] = isOptionalPositive
	vd["isOptionalNonNegative"] = isOptionalNonNegative
	vd["checkURL"] = checkURL
	if err := vd.Validate(msg); len(err) > 0 {
		return nil, err
	}

	// logic check: x,y,w,h are all-in or all-out
	if !((msg.Cx == "" && msg.Cy == "" && msg.Cw == "" && msg.Ch == "") ||
		(msg.Cx != "" && msg.Cy != "" && msg.Ch != "" && msg.Cw != "")) {
		return nil, []error{errors.New("must pass crop info together")}
	}

	return &msg, nil
}

func checkURL(i interface{}) error {
	input := i.(string)

	// validate it
	purl, err := url.Parse(input)
	if err != nil {
		return err
	}

	// for now
	if purl.Host != "www.youtube.com" {
		return errors.New("not youtube: " + purl.Host)
	}
	return nil

}

func isOptionalPositive(i interface{}) error {
	if i.(string) == "" {
		return nil
	}
	n, err := strconv.Atoi(i.(string))
	if err != nil {
		return err
	}

	if n <= 0 {
		return errors.New("must be positive")
	}
	return nil
}

func isOptionalNonNegative(i interface{}) error {
	if i.(string) == "" {
		return nil
	}
	n, err := strconv.Atoi(i.(string))
	if err != nil {
		return err
	}

	if n < 0 {
		return errors.New("must be positive")
	}
	return nil
}
