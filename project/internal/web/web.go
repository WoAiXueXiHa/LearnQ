package web

import (
	"embed"
	"html/template"
	"net/http"
)

//go:embed templates/* static/*
var assets embed.FS

func Handler() http.Handler {
	t := template.Must(template.ParseFS(assets, "templates/*.html"))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_ = t.ExecuteTemplate(w, "index.html", map[string]string{"Title": "LearnQ"})
	})
	mux.Handle("/static/", http.FileServer(http.FS(assets)))
	return mux
}
