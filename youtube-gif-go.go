package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Stantheman/youtube-gif-go/config"
	"github.com/Stantheman/youtube-gif-go/logger"
	"github.com/Stantheman/youtube-gif-go/redisPool"
	"github.com/garyburd/redigo/redis"
	"github.com/gorilla/mux"
	"net/http"
	"net/url"
	"os"
	"strconv"
)

// http://codegangsta.gitbooks.io/building-web-apps-with-go/

var l = logger.Get()

func main() {
	conf := config.Get()

	r := mux.NewRouter().StrictSlash(true)
	r.HandleFunc("/", HomeHandler)

	// Gifs collection
	gifs := r.Path("/gifs").Subrouter()
	gifs.Methods("GET").HandlerFunc(GifsIndexHandler)
	gifs.Methods("POST").HandlerFunc(GifsCreateHandler)

	// Gifs singular
	gif := r.PathPrefix(`/gifs/{id:\d+}/`).Subrouter()
	gif.Methods("GET").HandlerFunc(GifShowHandler)

	connect := conf.Site.Ip + ":" + conf.Site.Port
	fmt.Println("Starting server on " + connect)
	http.ListenAndServe(connect, r)
}

func HomeHandler(rw http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(rw, "Home")
}

// GifsIndexHandler: ask redis for a list of completed images
func GifsIndexHandler(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/html")
	// templating?
	fmt.Fprintln(rw, "gifs index<br/><br/>")

	c := redisPool.Pool.Get()
	defer c.Close()

	// cant tell if active image list is weird or redis
	if exists, _ := redis.Int(c.Do("EXISTS", "active_images")); exists != 1 {
		fmt.Fprintf(rw, "no images yet\n")
		l.Notice("checking active_images: none yet")
		return
	}

	images, err := redis.Strings(c.Do("LRANGE", "active_images", 0, -1))
	if err != nil {
		fmt.Fprintf(rw, "couldnt get images: %v\n", err.Error())
		l.Notice("getting images: " + err.Error())
		return
	}

	for _, image := range images {
		fmt.Fprintf(rw, `<a href="/gifs/%v/">%v</a><br/>`, image, image)
	}
}

/* GifsCreateHandler: take a URL over post and put it in a queue
Pass URL to this handler, it'll check that it looks like a URL
give a 202 ACK, and bounce*/
func GifsCreateHandler(rw http.ResponseWriter, r *http.Request) {

	url, err := parseParams(r)
	if err != nil {
		fmt.Fprintf(rw, "Couldn't add video to queue: %v\n", err)
		l.Err("adding video to queue: " + err.Error())
		return
	}

	id, err := addURLToRedis(url)
	if err != nil {
		fmt.Fprintf(rw, "Couldn't submit URL: %v\n", err)
		l.Err("submitting url: " + err.Error())
		return
	}
	// validate options, pass 202 Accepted
	rw.WriteHeader(202)
	fmt.Fprintf(rw, "id: %v\n", id)
}

// GifShowHandler: do we even want to hand back gifs? json?
func GifShowHandler(rw http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	c := redisPool.Pool.Get()
	defer c.Close()

	// freak if it ain't real
	status, err := redis.String(c.Do("HGET", "gif:"+id, "status"))
	if err != nil {
		fmt.Fprintf(rw, "gif doesnt exist\n")
		l.Notice("getting gif: " + err.Error())
		return
	}
	if status != "available" {
		fmt.Fprintf(rw, "status: %v\n", status)
		return
	}
	// verify that the file is actually there
	conf := config.Get()
	path := conf.Site.Gif_Dir + "/" + id + ".gif"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Fprintf(rw, "cant find gif\n")
		l.Notice("checking on disk path: " + err.Error())
		return
	}

	http.ServeFile(rw, r, path)
}

func parseParams(r *http.Request) (uri string, err error) {
	if err = r.ParseForm(); err != nil {
		l.Notice("parsing form: " + err.Error())
		// weird
		return "", err
	}

	// validate it
	purl, err := url.Parse(r.FormValue("url"))
	if err != nil {
		l.Notice("parsing url: " + err.Error())
		return "", err
	}

	// for now
	if purl.Host != "www.youtube.com" {
		l.Notice("checking host, isn't youtube: " + purl.Host)
		return "", errors.New("not youtube: " + purl.Host)
	}

	return r.FormValue("url"), nil
}

func addURLToRedis(url string) (id string, err error) {

	c := redisPool.Pool.Get()
	defer c.Close()

	// replace id increment with GUID?
	new_id, err := redis.Int(c.Do("INCR", "autoincr:gif"))
	if err != nil {
		l.Err("getting new id: " + err.Error())
		return "", err
	}
	keyname := "gif:" + strconv.Itoa(new_id)

	// set up the json bomb for pubsub. grab pubsubmessage from worker
	payload, err := json.Marshal(map[string]string{
		"url": url,
		"id":  strconv.Itoa(new_id),
	})
	if err != nil {
		l.Err("marshalling json: " + err.Error())
		return "", err
	}

	// pipelined transaction city
	c.Send("MULTI")
	c.Send("HSET", keyname, "origin", url)
	// enum/smarts for status/queue
	c.Send("HSET", keyname, "status", "pending download")
	c.Send("PUBLISH", "download-queue", payload)
	if _, err = c.Do("EXEC"); err != nil {
		l.Err("adding image to redis: " + err.Error())
		return "", err
	}

	return strconv.Itoa(new_id), nil
}
