package logger

import (
	"log/syslog"
	"runtime"
	"strconv"
)

var w *syslog.Writer

func Get() *syslog.Writer {
	// solve the stale filehandle problem
	// if w != nil {
	// 	return w
	// }
	w, err := syslog.New(syslog.LOG_ERR, "youtube-gif-go")
	if err != nil {
		panic(err)
	}
	_, file, line, _ := runtime.Caller(1)
	w.Info(file + ":" + strconv.Itoa(line) + ": Getting new logger")
	return w
}
