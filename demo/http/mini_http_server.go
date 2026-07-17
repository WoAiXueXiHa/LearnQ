package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type HealthResponse struct {
	Status string `json:"status"`
}

type HelloResponse struct {
	Message string `json:"message"`
}

type EchoResponse struct {
	Message string `json:"echo"`
}

type SumResponse struct {
	Result int `json:"result"`
}

type EchoBody struct {
	Message string `json:"message"`
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	// 只允许 GET 访问 /health
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 告诉客户端，我返回的是 JSON 格式
	w.Header().Set("Content-Type", "application/json")

	// 创建一个健康检查响应对象
	resp := HealthResponse{
		Status: "ok",
	}

	// 将响应对象编码为 JSON 并写入响应体
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Println("Error encoding JSON response:", err)
	}
}

func helloHandler(w http.ResponseWriter, r *http.Request) {
	// 只允许 GET 访问 /hello
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 从 URL 查询参数里读 name
	// 例如： /hello?name=Vect
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "world" // 如果没有提供 name 参数，默认使用 "world"
	}

	w.Header().Set("Content-Type", "application/json")

	// 创建一个问候响应对象
	resp := HelloResponse{
		Message: "hello, " + name + "!",
	}

	json.NewEncoder(w).Encode(resp)
	// log.Println("method:", r.Method)
	// log.Println("path:", r.URL.Path)
	// log.Println("raw query:", r.URL.RawQuery)
	// log.Println("name:", r.URL.Query().Get("name"))
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	msg := r.URL.Query().Get("msg")
	resp := EchoResponse{
		Message: msg,
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Println("Error encoding JSON response:", err)
	}
}

func sumHandler(w http.ResponseWriter, r *http.Request) {
	// 1. 只允许 GET 访问 /sum
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 2. 读取 aStr 和 bStr 参数
	aStr := r.URL.Query().Get("a")
	bStr := r.URL.Query().Get("b")

	// 3. 将 aStr 和 bStr 转换为整数
	a, err := strconv.Atoi(aStr)
	if err != nil {
		http.Error(w, "Invalid parameter 'a'", http.StatusBadRequest)
		return
	}

	b, err := strconv.Atoi(bStr)
	if err != nil {
		http.Error(w, "Invalid parameter 'b'", http.StatusBadRequest)
		return
	}

	// 4. 计算和
	result := a + b

	w.Header().Set("Content-Type", "application/json")

	// 5. 返回 JSON 响应
	resp := SumResponse{
		Result: result,
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Println("Error encoding JSON response:", err)
	}
}

func slowHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	log.Println("slow handler started")

	time.Sleep(3 * time.Second)

	w.Header().Set("Content-Type", "application/json")

	resp := map[string]string{
		"message": "slow done",
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Println("Error encoding JSON response:", err)
	}

	log.Println("slow handler finished")
}

func echoBodyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body EchoBody
	// 因为要修改结构体，所以要传指针
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Println("Error encoding JSON response:", err)
	}
}

func main() {
	// 创建一个独立的路由器，负责把不同路径分发给不同的 handler
	mux := http.NewServeMux()

	// 注册路由
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/hello", helloHandler)
	mux.HandleFunc("/echo", echoHandler)
	mux.HandleFunc("/sum", sumHandler)
	mux.HandleFunc("/echo_body", echoBodyHandler)
	mux.HandleFunc("/slow", slowHandler)

	// 创建 http server 服务
	server := &http.Server{
		Addr: ":8080", // 监听端口
		// 注释掉的话，就会404，
		// 理由：没有指定路由器处理，已经创建了一个路由器，
		// 并且注册了路由路径，那么就必须指定路由器处理请求
		Handler: mux,
	}

	go func() {
		log.Println("Starting server on :8080")

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("server error:", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatal("Server shutdown error:", err)
	}

	log.Println("Server gracefully stopped")
}
