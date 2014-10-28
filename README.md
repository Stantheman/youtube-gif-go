youtube-gif-go
==============

Toy project to turn videos into gifs, not on the commandline.

API
---

 * Post to /gifs, receive a Job ID and a Location header with the place to check for updates.
 * Poll /jobs/{id} to see the status of the job. Status "available" means it's ready, "failed" is bad.
 * Once available, /gifs/{id} should be a GIF

Components
----------

The API tries its best to be RESTful, taking a page from [this post on "RESTy long-ops"](http://billhiggins.us/blog/2011/04/27/resty-long-ops/). A POST request to /gifs returns 202 Accepted, and redirects the API user to the new job resource.

Once the request is received, the POST parameters are verified with the excellent [verify](https://github.com/mccoyst/validate) library. The resource ID is stored in Redis, and the combined POST message is serialized into JSON and is published to a Redis pubsub channel. The workers take over from here.

The work model is pipelined, meaning that each worker listens on its own queue for work, then does the work, and if successful, pushes the message along. Specifically for Youtube-Gif-Go:

 * The API sends the serialized JSON to the 'download-queue'
 * The "download" worker wakes up, downloads the video, and shoves the JSON onto the 'chop-queue'
 * The "chop" worker wakes up, turns the video into separate PNG files, and publishes on 'stitch-queue'
 * The "stitch" worker wakes up and turns the PNGs into a GIF, then publishes to 'finalize-queue'
 * The "finalize" worker wakes up, moves the GIF to its final destination and cleans up the work directories. It updates the status of the GIF in Redis

Each worker does its work in a GoRoutine and can handle several jobs at once.