package config

import (
	"code.google.com/p/gcfg"
	"fmt"
	"os"
)

type Config struct {
	Worker struct {
		Dir string
	}
	Site struct {
		Port    string
		Ip      string
		Gif_Dir string
	}
	Redis struct {
		Port string
		Ip   string
	}
}

var conf Config

func Get() Config {
	// if we loaded it before, return that instead
	if conf.Worker.Dir != "" {
		return conf
	}
	fmt.Println(os.Getwd())
	if err := gcfg.ReadFileInto(&conf, "config.txt"); err != nil {
		panic(err)
	}
	return conf
}
