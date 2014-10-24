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

	jobname, err := get_worker_name()
	if err != nil {
		return
	}

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
			// decode json
			payload := api.PubSubMessage{}
			if err := json.Unmarshal(v.Data, &payload); err != nil {
				l.Err(err.Error())
				continue
			}
			// do work
			if err := work(payload, jobname); err != nil {
				l.Err(err.Error())
				markFailed(payload.ID, jobname, err.Error())
			}
		case redis.Subscription:
			fmt.Printf("%s: %s %d\n", v.Channel, v.Kind, v.Count)
		case error:
			fmt.Println(v.Error())
			return
		}
	}
}

func get_worker_name() (string, error) {
	if len(os.Args) != 2 {
		fmt.Println("Usage: " + os.Args[0] + " worker-name")
		return "", err
	}
	if _, ok := jobs[os.Args[1]]; ok != true {
		fmt.Println("Usage: " + os.Args[0] + " worker-name")
		return "", err
	}

	return os.Args[1], nil
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
	c.Send("HSET", keyname, "status", "failed at "+job+". Error: "+errormsg)
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
		"--max-filesize", "10M",
		"--output", outFile,
		"--write-info-json",
		"--quiet",
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
	// need to think more about more specific flags
	// -sstep looks neat
	//  When used in conjunction with -ss option, -endpos time will shift forward
	// by seconds specified with -ss if not a byte position.
	// crop[=w:h:x:y] rectangle[=w:h:x:y]
	// scale[=w:h[:interlaced[:chr_drop[:par[:par2[:presize[:noup[:arnd]]]]]]]]
	var flags []string
	if payload.Dur != "" {
		flags = []string{"-endpos", payload.Dur}
	}

	flags = append(flags,
		"-ss", payload.Start,
		"-vo", "png:z=9:outdir="+workspace,
		"-ao", "null",
		"-vf", "crop="+payload.Cw+":"+payload.Ch+":"+payload.Cx+":"+payload.Cy,
		payload.PrevDir+payload.ID+".youtube",
	)

	cmd := exec.Command("/usr/bin/mplayer", flags...)

	if output, err := cmd.CombinedOutput(); err != nil {
		return errors.New(err.Error() + ". " + string(output))
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
func stitch(payload api.PubSubMessage, workspace string) error {
	gifname := workspace + payload.ID + ".gif"

	// should be configurable, make proper tmpdir etc
	// mad lazy, shell + constant filename
	cmd := exec.Command("/bin/bash", "-c",
		"/usr/bin/convert +repage -fuzz 1.6% -delay 4 -loop 0 "+payload.PrevDir+"*.png "+
			"-layers OptimizePlus -layers OptimizeTransparency gif:- "+
			"| /usr/bin/gifsicle -O3 --colors 256 > "+gifname,
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
