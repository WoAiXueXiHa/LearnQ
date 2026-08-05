// Package web 把前端模板与静态资源嵌入二进制，以标准库 http.Handler 形式暴露给 Gin。
package web

import (
	"embed"
	"html/template"
	"net/http"
)

// 编译期嵌入 templates/ 与 static/ 全部文件，交付物只有单个可执行文件，无需静态目录。
//
//go:embed templates/* static/*
var assets embed.FS

// Handler 返回根 ServeMux：/ 渲染 index.html，/static/ 由嵌入 FS 提供静态文件。
func Handler() http.Handler {
	// 前端资源嵌入二进制，API 部署不依赖额外静态目录；模板仍使用 html/template 自动转义。
	// Must 让模板语法错误在启动期直接 panic，避免运行时才暴露。
	t := template.Must(template.ParseFS(assets, "templates/*.html"))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 未匹配 /static/ 的任意路径都渲染 index.html：前端是单页应用，视图切换由 JS 完成，不依赖服务端路由。
		_ = t.ExecuteTemplate(w, "index.html", map[string]string{"Title": "LearnQ"})
	})
	mux.Handle("/static/", http.FileServer(http.FS(assets)))
	return mux
}
