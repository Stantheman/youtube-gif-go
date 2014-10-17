package main

import (
	"encoding/json"
	"fmt"
	"github.com/Stantheman/youtube-gif-go/config"
	"github.com/Stantheman/youtube-gif-go/redisPool"
	"github.com/garyburd/redigo/redis"
	"os"
	"os/exec"
)

// pubsubmessages is the structure after decoding the JSON
// published via Redis
type pubsubmessage map[string]string

// workerfunc takes a decoded message and does work on it
type workerfunc func(payload pubsubmessage, workdir string) error

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
		tell: "",
	},
}

var conf = config.Get()

func main() {
	// get the worker name
	if len(os.Args) != 2 {
		fmt.Println("Usage: " + os.Args[0] + " worker-name")
		return
	}
	if _, ok := jobs[os.Args[1]]; ok != true {
		fmt.Println("Usage: " + os.Args[0] + " worker-name")
		return
	}

	jobname := os.Args[1]

	// set up the workdir
	if err := os.MkdirAll(conf.Worker.Dir, os.ModeDir); err != nil {
		fmt.Println(err)
		return
	}

	// each worker listens to its -queue for work
	queue := jobname + "-queue"
	// get a nicely wrapped pubsub connection
	psc := redis.PubSubConn{redisPool.Pool.Get()}
	defer psc.Close()

	if err := psc.Subscribe(queue); err != nil {
		fmt.Printf("Can't subscribe to the %v: %v\n", queue, err.Error())
		return
	}

	for {
		switch v := psc.Receive().(type) {
		case redis.Message:
			if err := work(v.Data, jobname); err != nil {
				fmt.Println(err)
				return
			}
		case redis.Subscription:
			fmt.Printf("%s: %s %d\n", v.Channel, v.Kind, v.Count)
		case error:
			fmt.Println(v.Error())
			return
		}
	}
}

/* update tells redis about updates to images, including status,
sending pubsub messages, and potentially adding the image to
the list of active images.
It takes both the serialized and unserialized json data for easy resubmission.
Not sure how I feel about that, but doing the work twice seems silly too.
I make up for feeling bad by thinking I could be passing different data for the
next job*/
func update(data []byte, payload pubsubmessage, job string) (err error) {
	// get ready to update the image status and continue pubsubbin
	c := redisPool.Pool.Get()
	keyname := "gif:" + payload["id"]

	// if there's no one left to tell, we're done processing
	if jobs[job].tell == "" {
		// Add the image to active_image list, set image to avail
		c.Send("MULTI")
		c.Send("RPUSH", "active_images", payload["id"])
		c.Send("HSET", keyname, "status", "available")
		if _, err := c.Do("EXEC"); err != nil {
			return err
		}
		return nil
	}

	// otherwise we need to update status and tell people
	c.Send("MULTI")
	c.Send("HSET", keyname, "status", "pending "+jobs[job].tell)
	c.Send("PUBLISH", jobs[job].tell+"-queue", data)
	if _, err := c.Do("EXEC"); err != nil {
		return err
	}
	return nil
}

/* work is a wrapper method that does the setup and teardown around jobs */
func work(data []byte, job string) (err error) {
	// decode json
	payload := make(map[string]string)
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	workspace := conf.Worker.Dir + "/" + payload["id"] + "/"

	// make workspace
	if err := os.MkdirAll(workspace, 0777); err != nil {
		return err
	}

	// do the job
	if err := jobs[job].proc(payload, workspace); err != nil {
		return err
	}

	// update the status
	if err := update(data, payload, job); err != nil {
		return err
	}

	return nil
}

/* download uses youtube-dl to take a youtube URL and place it on disk */
func download(payload pubsubmessage, workspace string) error {

	// 1.youtube
	outFile := workspace + payload["id"] + ".youtube"

	// needs config for youtube-dl path
	cmd := exec.Command("/opt/youtube-dl",
		"--no-progress",
		"--max-filesize", "10M",
		"--output", outFile,
		"--write-info-json",
		"--quiet",
		"--format", "worstvideo",
		payload["url"],
	)

	// needs logging, stdout
	if err := cmd.Run(); err != nil {
		return err
	}

	// verify that the file is actually there
	if _, err := os.Stat(outFile); os.IsNotExist(err) {
		return err
	}

	return nil
}

/* chop takes a workspace directory with an <id>.youtube file
and uses mplayer to create a PNG file for every frame in the video*/
func chop(payload pubsubmessage, workspace string) error {

	// https://www.youtube.com/watch?v=dgKGixi8bp8 short video
	// need to think more about more specific flags
	cmd := exec.Command("/usr/bin/mplayer",
		"-ss", "0",
		"-vo", "png:z=9:outdir="+workspace,
		"-ao", "null",
		"-really-quiet",
		workspace+payload["id"]+".youtube",
	)

	if err := cmd.Run(); err != nil {
		return err
	}

	// bash script would rm [0,2,4,6,8].png to make gifmaking quicker, gif smaller
	// think about doing that at some boundary

	// mplayer uses the following format to make pngs, so the 00000001.png assumption
	// is technically...ok. replace w/ subdir for pngs, subdir per job type?
	//   snprintf (buf, 100, "%s/%s%08d.png", png_outdir, png_outfile_prefix, ++framenum);
	// verify that the file is actually there
	if _, err := os.Stat(workspace + "00000001.png"); os.IsNotExist(err) {
		return err
	}

	return nil
}

/* stitch takes a workspace directory of PNG files and transforms them
into a gif on STDOUT using imagemagick, which is piped into
gifsicle for optimization */
func stitch(payload pubsubmessage, workspace string) error {

	// hmmmmm
	gifname := "/srv/git/go/src/github.com/Stantheman/youtube-gif-go/gifs/" +
		payload["id"] + ".gif"

	// should be configurable, make proper tmpdir etc
	// mad lazy, shell + constant filename
	cmd := exec.Command("/bin/bash", "-c",
		"/usr/bin/convert +repage -fuzz 1.6% -delay 6 -loop 0 "+workspace+"*.png "+
			"-layers OptimizePlus -layers OptimizeTransparency gif:- "+
			"| /usr/bin/gifsicle -O3 --colors 256 > "+gifname,
	)

	if err := cmd.Run(); err != nil {
		return err
	}

	// verify that the file is actually there
	if _, err := os.Stat(gifname); os.IsNotExist(err) {
		return err
	}

	return nil
}
