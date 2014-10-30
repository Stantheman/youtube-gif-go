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
	"os"
	"os/exec"
)

// workerfunc takes a decoded message and does work on it
type workerfunc func(payload api.PubSubMessage, workdir string) error

type worker struct {
	proc workerfunc
	tell string
}

var jobs = map[string]worker{
	"download": {
		proc: download,
		tell: "chop",
	},
	"chop": {
		proc: chop,
		tell: "stitch",
	},
	"stitch": {
		proc: stitch,
		tell: "finalize",
	},
	"finalize": {
		proc: finalize,
		tell: "",
	},
}

var conf = config.Get()
var l = logger.Get()

func main() {

	jobname := get_worker_name()

	// each worker listens to its -queue for work
	queue := jobname + "-queue"

	// get a nicely wrapped pubsub connection
	psc := redis.PubSubConn{redisPool.Pool.Get()}
	defer psc.Close()

	if err := psc.Subscribe(queue); err != nil {
		l.Err("Can't subscribe to the " + queue + ": " + err.Error())
		return
	}

	for {
		switch v := psc.Receive().(type) {
		case redis.Message:
			go process_message(v.Data, jobname)
		case redis.Subscription:
			fmt.Printf("%s: %s %d\n", v.Channel, v.Kind, v.Count)
		case error:
			fmt.Println(v.Error())
			return
		}
	}
}

func get_worker_name() string {
	if len(os.Args) != 2 {
		fmt.Println("Usage: " + os.Args[0] + " worker-name")
		os.Exit(1)
	}
	if _, ok := jobs[os.Args[1]]; ok != true {
		fmt.Println("Usage: " + os.Args[0] + " worker-name")
		os.Exit(1)
	}

	return os.Args[1]
}

func process_message(data []byte, jobname string) {
	// decode json
	payload := api.PubSubMessage{}
	if err := json.Unmarshal(data, &payload); err != nil {
		l.Err(err.Error())
		return
	}
	// do work
	if err := work(payload, jobname); err != nil {
		l.Err(err.Error())
		markFailed(payload.ID, jobname, err.Error())
	}
}

/* work is a wrapper method that does the setup and teardown around jobs */
func work(payload api.PubSubMessage, job string) (err error) {

	// mark active
	if err := markInProgress(payload.ID, job); err != nil {
		markFailed(payload.ID, job, err.Error())
		return err
	}

	workspace := conf.Worker.Dir + "/" + payload.ID + "/" + job + "/"

	// make workspace
	if err := os.MkdirAll(workspace, 0777); err != nil {
		return err
	}

	// do the job
	if err := jobs[job].proc(payload, workspace); err != nil {
		return err
	}

	// update the status
	if err := update(payload, job, workspace); err != nil {
		return err
	}

	return nil
}

func update(payload api.PubSubMessage, job, workspace string) (err error) {

	// get ready to update the image status and continue pubsubbin
	c := redisPool.Pool.Get()
	keyname := "gif:" + payload.ID

	// if there's no one left to tell, we're done processing
	if jobs[job].tell == "" {
		// Add the image to active_image list, set image to avail
		c.Send("MULTI")
		c.Send("RPUSH", "active_images", payload.ID)
		c.Send("HSET", keyname, "status", "available")
		c.Send("PERSIST", keyname)
		if _, err := c.Do("EXEC"); err != nil {
			return err
		}
		return nil
	}
	// otherwise we need to update status and tell people

	// push the new workspace onto the stack
	payload.PrevDir = workspace

	// serialize payload for sending back out
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	c.Send("MULTI")
	c.Send("HSET", keyname, "status", "pending "+jobs[job].tell)
	c.Send("EXPIRE", keyname, 3600)
	c.Send("PUBLISH", jobs[job].tell+"-queue", data)
	if _, err := c.Do("EXEC"); err != nil {
		return err
	}
	return nil
}

func markInProgress(id, job string) error {
	c := redisPool.Pool.Get()
	keyname := "gif:" + id

	c.Send("MULTI")
	c.Send("HSET", keyname, "status", job+"-ifying")
	c.Send("EXPIRE", keyname, 3600)
	if _, err := c.Do("EXEC"); err != nil {
		return err
	}
	return nil
}

func markFailed(id, job, errormsg string) {
	c := redisPool.Pool.Get()
	keyname := "gif:" + id

	c.Send("MULTI")
	c.Send("HSET", keyname, "status", "failed")
	c.Send("HSET", keyname, "description", errormsg)
	c.Send("PERSIST", keyname)
	if _, err := c.Do("EXEC"); err != nil {
		// since this is the failure marker, log it and move on
		l.Err("Failed on trying to update failure for " + id + " (" + job + ". Moving on")
	}
	l.Err("Making " + id + " as failed in job " + job + ".")
}

/* download uses youtube-dl to take a youtube URL and place it on disk */
func download(payload api.PubSubMessage, workspace string) error {

	// 1.youtube
	outFile := workspace + payload.ID + ".youtube"

	// needs config for youtube-dl path
	cmd := exec.Command("/opt/youtube-dl",
		"--no-progress",
		"--max-filesize", "100M",
		"--output", outFile,
		"--write-info-json",
		"--format", "mp4",
		payload.URL,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		return errors.New(err.Error() + ". " + string(output))
	}
	// verify that the file is actually there
	if _, err := os.Stat(outFile); os.IsNotExist(err) {
		return err
	}

	return nil
}

/* chop takes a workspace directory with an <id>.youtube file
and uses mplayer to create a PNG file for every frame in the video*/
func chop(payload api.PubSubMessage, workspace string) error {

	// https://www.youtube.com/watch?v=dgKGixi8bp8 short video
	var flags = []string{}

	var wscale, hscale, xcoord, ycoord, swidth, sheight string

	// avconv allows for math based on frame size
	// scale the crop size/coords against the real one
	if payload.Cx != "" {
		wscale = "(in_w/" + payload.Vw + ")*" + payload.Cw
		hscale = "(in_h/" + payload.Vh + ")*" + payload.Ch
		xcoord = "(in_w/" + payload.Vw + ")*" + payload.Cx
		ycoord = "(in_h/" + payload.Vh + ")*" + payload.Cy
		swidth = payload.Cw
		sheight = payload.Ch
	} else {
		swidth = payload.Vw
		sheight = payload.Vh
	}

	flags = append(flags,
		"-i", payload.PrevDir+payload.ID+".youtube",
		"-vf", "crop="+wscale+":"+hscale+":"+xcoord+":"+ycoord+",scale="+swidth+":"+sheight,
	)
	if payload.Dur != "" {
		flags = append(flags,
			"-ss", payload.Start,
			"-t", payload.Dur,
		)
	}
	flags = append(flags,
		workspace+"%08d.png",
	)

	cmd := exec.Command("/usr/local/bin/avconv", flags...)

	if output, err := cmd.CombinedOutput(); err != nil {
		return errors.New(err.Error() + ". " + string(output))
	}

	// bash script would rm [0,2,4,6,8].png to make gifmaking quicker, gif smaller
	// think about doing that at some boundary
	if _, err := os.Stat(workspace + "00000001.png"); os.IsNotExist(err) {
		return err
	}

	return nil
}

/* stitch takes a workspace directory of PNG files and transforms them
into a gif on STDOUT using imagemagick, which is piped into
gifsicle for optimization */
func stitch(payload api.PubSubMessage, workspace string) error {
	gifname := workspace + payload.ID + ".gif"

	// -monitor
	cmd := exec.Command("/bin/bash", "-c",
		"/usr/bin/gm convert +repage -fuzz 1.6% -delay 4 -loop 0 "+payload.PrevDir+"*.png "+
			"gif:-|/usr/bin/gifsicle -O3 --colors 256 > "+gifname,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		return errors.New(err.Error() + ". " + string(output))
	}

	// verify that the file is actually there
	if _, err := os.Stat(gifname); os.IsNotExist(err) {
		return err
	}

	return nil
}

func finalize(payload api.PubSubMessage, workspace string) error {
	gifname := conf.Site.Gif_Dir + "/" + payload.ID + ".gif"

	if err := os.Rename(payload.PrevDir+payload.ID+".gif", gifname); err != nil {
		return err
	}

	if err := os.RemoveAll(conf.Worker.Dir + "/" + payload.ID); err != nil {
		l.Err("Couldn't remove old directory: " + err.Error())
	}

	return nil
}
