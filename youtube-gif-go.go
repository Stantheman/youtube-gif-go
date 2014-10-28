package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Stantheman/youtube-gif-go/api"
	"github.com/Stantheman/youtube-gif-go/config"
	"github.com/Stantheman/youtube-gif-go/logger"
	"github.com/Stantheman/youtube-gif-go/redisPool"
	"github.com/garyburd/redigo/redis"
	"github.com/gorilla/mux"
	"github.com/unrolled/render"
	"net/http"
	"os"
	"strconv"
)

// http://codegangsta.gitbooks.io/building-web-apps-with-go/

var l = logger.Get()
var rend = render.New(render.Options{})

func main() {
	conf := config.Get()

	r := mux.NewRouter().StrictSlash(true)
	r.HandleFunc("/", HomeHandler)

	// Gifs collection
	gifs := r.Path("/gifs").Subrouter()
	gifs.Methods("GET").HandlerFunc(GifsIndexHandler)
	gifs.Methods("POST").HandlerFunc(GifsCreateHandler)

	// Gifs singular
	gif := r.PathPrefix(`/gifs/{id:\d+}`).Subrouter()
	gif.Methods("GET").HandlerFunc(GifShowHandler)

	// Job singular
	job := r.PathPrefix(`/jobs/{id:\d+}`).Subrouter()
	job.Methods("GET").HandlerFunc(JobShowHandler)

	connect := conf.Site.Ip + ":" + conf.Site.Port
	fmt.Println("Starting server on " + connect)
	http.ListenAndServe(connect, r)
}

func HomeHandler(rw http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(rw, "Home")
}

// GifsIndexHandler: ask redis for a list of completed images
func GifsIndexHandler(rw http.ResponseWriter, r *http.Request) {

	c := redisPool.Pool.Get()
	defer c.Close()

	// cant tell if active image list is weird or redis
	if exists, _ := redis.Int(c.Do("EXISTS", "active_images")); exists != 1 {
		rend.JSON(rw, http.StatusNoContent, jsonErr(errors.New("no images")))
		l.Notice("checking active_images: none yet")
		return
	}

	images, err := redis.Strings(c.Do("LRANGE", "active_images", 0, -1))
	if err != nil {
		rend.JSON(rw, http.StatusInternalServerError, jsonErr(err))
		l.Notice("getting images: " + err.Error())
		return
	}

	jlist := make(map[string]string)
	for _, image := range images {
		jlist[image] = "/gifs/" + image
	}
	rend.JSON(rw, http.StatusOK, jlist)
}

/* GifsCreateHandler: take a URL over post and put it in a queue
Pass URL to this handler, it'll check that it looks like a URL
give a 202 ACK, and bounce*/
func GifsCreateHandler(rw http.ResponseWriter, r *http.Request) {

	rw.Header().Set("Access-Control-Allow-Origin", "*")
	if err := r.ParseForm(); err != nil {
		l.Notice("parsing form: " + err.Error())
		rend.JSON(rw, http.StatusBadRequest, jsonErr(err))
		return
	}

	msg, errs := api.ValidateParams(r.Form)
	if len(errs) > 0 {
		var probs []string = make([]string, 0)
		for _, prob := range errs {
			probs = append(probs, prob.Error())
		}
		rend.JSON(rw, http.StatusBadRequest, map[string][]string{"errors": probs})
		return
	}

	id, err := addURLToRedis(msg)
	if err != nil {
		rend.JSON(rw, http.StatusInternalServerError, jsonErr(err))
		return
	}
	// validate options, pass 202 Accepted
	rw.Header().Set("Location", "/jobs/"+id)
	rend.JSON(rw, http.StatusAccepted, map[string]string{"id": id})
}

// GifShowHandler: do we even want to hand back gifs? json?
func GifShowHandler(rw http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	c := redisPool.Pool.Get()
	defer c.Close()

	rw.Header().Set("Access-Control-Allow-Origin", "*")

	// verify that the file is actually there
	conf := config.Get()
	path := conf.Site.Gif_Dir + "/" + id + ".gif"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		rend.JSON(rw, http.StatusNotFound, jsonErr(errors.New("file doesn't exist")))
		l.Err("checking on disk path: " + err.Error())
		return
	}

	http.ServeFile(rw, r, path)
}

func JobShowHandler(rw http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	c := redisPool.Pool.Get()
	defer c.Close()

	rw.Header().Set("Access-Control-Allow-Origin", "*")

	// freak if it ain't real
	resp, err := redis.Strings(c.Do("HMGET", "gif:"+id, "status", "description"))
	if err != nil {
		rend.JSON(rw, http.StatusBadRequest, jsonErr(errors.New("gif doesn't exist")))
		l.Notice("getting gif: " + err.Error())
		return
	}

	rend.JSON(rw, http.StatusOK, map[string]string{"status": resp[0], "description": resp[1]})
}

func addURLToRedis(msg *api.PubSubMessage) (id string, err error) {

	c := redisPool.Pool.Get()
	defer c.Close()

	// replace id increment with GUID?
	new_id, err := redis.Int(c.Do("INCR", "autoincr:gif"))
	if err != nil {
		l.Err("getting new id: " + err.Error())
		return "", err
	}
	msg.ID = strconv.Itoa(new_id)
	keyname := "gif:" + msg.ID

	// set up the json bomb for pubsub. grab pubsubmessage from worker
	payload, err := json.Marshal(msg)
	if err != nil {
		l.Err("marshalling json: " + err.Error())
		return "", err
	}

	// pipelined transaction city
	c.Send("MULTI")
	c.Send("HSET", keyname, "origin", msg.URL)
	// enum/smarts for status/queue
	c.Send("HSET", keyname, "status", "pending download")
	// one shot, do not miss your chance to blow
	c.Send("EXPIRE", keyname, 3600)
	c.Send("PUBLISH", "download-queue", payload)
	if _, err = c.Do("EXEC"); err != nil {
		l.Err("adding image to redis: " + err.Error())
		return "", err
	}

	return msg.ID, nil
}

func jsonErr(msg error) (res map[string]string) {
	return map[string]string{
		"error": msg.Error(),
	}
}
