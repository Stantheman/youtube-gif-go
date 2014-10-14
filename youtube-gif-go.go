package main

import (
	"fmt"
	"github.com/gorilla/mux"
	"net/http"
)

func main() {
	r := mux.NewRouter().StrictSlash(true)
	r.HandleFunc("/", HomeHandler)

	// Gifs collection
	gifs := r.Path("/gifs").Subrouter()
	gifs.Methods("GET").HandlerFunc(GifsIndexHandler)
	gifs.Methods("POST").HandlerFunc(GifsCreateHandler)

	// Gifs singular
	gif := r.PathPrefix("/gifs/{id}/").Subrouter()
	gif.Methods("GET").HandlerFunc(GifShowHandler)
	gif.Methods("DELETE").HandlerFunc(GifDeleteHandler)

	fmt.Println("Starting server on :8080")
	http.ListenAndServe(":8080", r)
}

func HomeHandler(rw http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(rw, "Home")
}

func GifsIndexHandler(rw http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(rw, "gifs index")
}

func GifsCreateHandler(rw http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(rw, "gifs create")
}

func GifShowHandler(rw http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	fmt.Fprintln(rw, "showing gif", id)
}

func GifDeleteHandler(rw http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(rw, "gif delete")
}
